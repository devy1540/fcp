package fcp;

import com.google.api.gax.core.NoCredentialsProvider;
import com.google.api.gax.grpc.GrpcTransportChannel;
import com.google.api.gax.rpc.FixedTransportChannelProvider;
import com.google.cloud.NoCredentials;
import com.google.auth.ServiceAccountSigner;
import com.google.cloud.iam.credentials.v1.IamCredentialsClient;
import com.google.cloud.iam.credentials.v1.IamCredentialsSettings;
import com.google.cloud.iam.credentials.v1.SignBlobRequest;
import com.google.cloud.pubsub.v1.SubscriptionAdminClient;
import com.google.cloud.pubsub.v1.SubscriptionAdminSettings;
import com.google.cloud.pubsub.v1.TopicAdminClient;
import com.google.cloud.pubsub.v1.TopicAdminSettings;
import com.google.cloud.storage.BlobId;
import com.google.cloud.storage.BlobInfo;
import com.google.cloud.storage.Storage;
import com.google.cloud.storage.StorageOptions;
import com.google.cloud.storage.PostPolicyV4;
import com.google.protobuf.FieldMask;
import com.google.pubsub.v1.DeadLetterPolicy;
import com.google.pubsub.v1.Subscription;
import com.google.pubsub.v1.SubscriptionName;
import com.google.pubsub.v1.Topic;
import com.google.pubsub.v1.TopicName;
import com.google.pubsub.v1.UpdateSubscriptionRequest;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import com.google.protobuf.ByteString;
import org.junit.jupiter.api.Test;

import java.nio.charset.StandardCharsets;
import java.net.HttpURLConnection;
import java.net.URI;
import java.net.URL;
import java.io.ByteArrayOutputStream;
import java.io.OutputStream;
import java.util.concurrent.TimeUnit;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;

class JavaSdkCompatibilityTest {
    private static final String PROJECT = "fcp-local";

    @Test
    void storageAndPubSubAdminUseOfficialDemoVersions() throws Exception {
        String httpEndpoint = required("FCP_HTTP_ENDPOINT");
        String grpcEndpoint = required("FCP_GCP_ENDPOINT");
		String suffix = Long.toUnsignedString(System.nanoTime());
        Storage storage = StorageOptions.newBuilder()
                .setProjectId(PROJECT).setHost(httpEndpoint).setCredentials(NoCredentials.getInstance())
                .build().getService();
		String bucket = "java-sdk-assets-" + suffix;
		storage.create(com.google.cloud.storage.BucketInfo.of(bucket));
        byte[] body = "java sdk".getBytes(StandardCharsets.UTF_8);
		BlobId blobId = BlobId.of(bucket, "reports/result.txt");
        storage.create(BlobInfo.newBuilder(blobId).setContentType("text/plain").build(), body);
        assertArrayEquals(body, storage.readAllBytes(blobId));

        ManagedChannel channel = ManagedChannelBuilder.forTarget(grpcEndpoint).usePlaintext().build();
        var transport = FixedTransportChannelProvider.create(GrpcTransportChannel.create(channel));
		var iamSettings = IamCredentialsSettings.newBuilder()
				.setTransportChannelProvider(transport)
				.setCredentialsProvider(NoCredentialsProvider.create()).build();
		try (IamCredentialsClient iam = IamCredentialsClient.create(iamSettings)) {
			String email = "fcp-storage-signer@fcp-local.iam.gserviceaccount.com";
			ServiceAccountSigner signer = new ServiceAccountSigner() {
				@Override public String getAccount() { return email; }
				@Override public byte[] sign(byte[] bytes) {
					return iam.signBlob(SignBlobRequest.newBuilder()
							.setName("projects/-/serviceAccounts/" + email)
							.setPayload(ByteString.copyFrom(bytes)).build()).getSignedBlob().toByteArray();
				}
			};
			URL signed = storage.signUrl(BlobInfo.newBuilder(blobId).build(), 5, TimeUnit.MINUTES,
					Storage.SignUrlOption.withV4Signature(), Storage.SignUrlOption.signWith(signer),
					Storage.SignUrlOption.withHostName(httpEndpoint), Storage.SignUrlOption.withPathStyle());
			HttpURLConnection connection = (HttpURLConnection) signed.openConnection();
			assertEquals(200, connection.getResponseCode());
			assertArrayEquals(body, connection.getInputStream().readAllBytes());
			PostPolicyV4 policy = storage.generateSignedPostPolicyV4(
					BlobInfo.newBuilder(BlobId.of(bucket, "uploads/form.txt")).build(), 5, TimeUnit.MINUTES,
					PostPolicyV4.PostFieldsV4.newBuilder().setContentType("text/plain").build(),
					PostPolicyV4.PostConditionsV4.newBuilder().addContentLengthRangeCondition(1, 1024).build(),
					Storage.PostPolicyV4Option.signWith(signer),
					Storage.PostPolicyV4Option.withBucketBoundHostname(
							URI.create(httpEndpoint).getAuthority(), Storage.UriScheme.HTTP));
			assertEquals(true, policy.getUrl().toString().startsWith(httpEndpoint));
			postPolicy(policy, "form upload".getBytes(StandardCharsets.UTF_8));
			assertArrayEquals("form upload".getBytes(StandardCharsets.UTF_8),
					storage.readAllBytes(BlobId.of(bucket, "uploads/form.txt")));
		}
        var topicSettings = TopicAdminSettings.newBuilder().setTransportChannelProvider(transport).setCredentialsProvider(NoCredentialsProvider.create()).build();
        var subscriptionSettings = SubscriptionAdminSettings.newBuilder().setTransportChannelProvider(transport).setCredentialsProvider(NoCredentialsProvider.create()).build();
        try (TopicAdminClient topics = TopicAdminClient.create(topicSettings);
             SubscriptionAdminClient subscriptions = SubscriptionAdminClient.create(subscriptionSettings)) {
			TopicName topic = TopicName.of(PROJECT, "java-jobs-" + suffix);
			TopicName dlq = TopicName.of(PROJECT, "java-jobs-dlq-" + suffix);
            topics.createTopic(Topic.newBuilder().setName(topic.toString()).build());
            topics.createTopic(Topic.newBuilder().setName(dlq.toString()).build());
			SubscriptionName subscription = SubscriptionName.of(PROJECT, "java-worker-" + suffix);
            subscriptions.createSubscription(Subscription.newBuilder()
                    .setName(subscription.toString()).setTopic(topic.toString()).setAckDeadlineSeconds(10).build());
            Subscription updated = subscriptions.updateSubscription(UpdateSubscriptionRequest.newBuilder()
                    .setSubscription(Subscription.newBuilder().setName(subscription.toString())
                            .setDeadLetterPolicy(DeadLetterPolicy.newBuilder().setDeadLetterTopic(dlq.toString()).setMaxDeliveryAttempts(5)))
                    .setUpdateMask(FieldMask.newBuilder().addPaths("dead_letter_policy")).build());
            assertEquals(dlq.toString(), updated.getDeadLetterPolicy().getDeadLetterTopic());
            assertEquals(5, updated.getDeadLetterPolicy().getMaxDeliveryAttempts());
            assertEquals(topic.toString(), topics.getTopic(topic).getName());
        } finally {
            channel.shutdownNow();
        }
    }

    private static String required(String name) {
        String value = System.getenv(name);
        if (value == null || value.isBlank()) throw new IllegalStateException(name + " is required");
        return value;
    }

	private static void postPolicy(PostPolicyV4 policy, byte[] file) throws Exception {
		String boundary = "----fcp-java-sdk-boundary";
		ByteArrayOutputStream body = new ByteArrayOutputStream();
		for (Map.Entry<String, String> field : policy.getFields().entrySet()) {
			body.write(("--" + boundary + "\r\nContent-Disposition: form-data; name=\"" + field.getKey()
					+ "\"\r\n\r\n" + field.getValue() + "\r\n").getBytes(StandardCharsets.UTF_8));
		}
		body.write(("--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"form.txt\""
				+ "\r\nContent-Type: text/plain\r\n\r\n").getBytes(StandardCharsets.UTF_8));
		body.write(file);
		body.write(("\r\n--" + boundary + "--\r\n").getBytes(StandardCharsets.UTF_8));
		HttpURLConnection connection = (HttpURLConnection) new URL(policy.getUrl()).openConnection();
		connection.setRequestMethod("POST");
		connection.setDoOutput(true);
		connection.setRequestProperty("Content-Type", "multipart/form-data; boundary=" + boundary);
		try (OutputStream output = connection.getOutputStream()) {
			body.writeTo(output);
		}
		assertEquals(204, connection.getResponseCode());
	}
}
