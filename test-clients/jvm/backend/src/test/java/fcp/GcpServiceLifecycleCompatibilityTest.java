package fcp;

import com.google.api.gax.core.NoCredentialsProvider;
import com.google.api.gax.grpc.GrpcTransportChannel;
import com.google.api.gax.rpc.FixedTransportChannelProvider;
import com.google.cloud.iam.credentials.v1.GenerateAccessTokenRequest;
import com.google.cloud.iam.credentials.v1.GenerateIdTokenRequest;
import com.google.cloud.iam.credentials.v1.IamCredentialsClient;
import com.google.cloud.iam.credentials.v1.IamCredentialsSettings;
import com.google.cloud.iam.credentials.v1.SignBlobRequest;
import com.google.cloud.iam.credentials.v1.SignJwtRequest;
import com.google.cloud.kms.v1.AsymmetricSignRequest;
import com.google.cloud.kms.v1.CreateCryptoKeyRequest;
import com.google.cloud.kms.v1.CreateCryptoKeyVersionRequest;
import com.google.cloud.kms.v1.CreateKeyRingRequest;
import com.google.cloud.kms.v1.CryptoKey;
import com.google.cloud.kms.v1.CryptoKeyVersion;
import com.google.cloud.kms.v1.CryptoKeyVersionTemplate;
import com.google.cloud.kms.v1.DecryptRequest;
import com.google.cloud.kms.v1.Digest;
import com.google.cloud.kms.v1.EncryptRequest;
import com.google.cloud.kms.v1.GetCryptoKeyRequest;
import com.google.cloud.kms.v1.GetCryptoKeyVersionRequest;
import com.google.cloud.kms.v1.GetKeyRingRequest;
import com.google.cloud.kms.v1.GetPublicKeyRequest;
import com.google.cloud.kms.v1.KeyManagementServiceClient;
import com.google.cloud.kms.v1.KeyManagementServiceSettings;
import com.google.cloud.kms.v1.KeyRing;
import com.google.cloud.kms.v1.ListCryptoKeyVersionsRequest;
import com.google.cloud.kms.v1.ListCryptoKeysRequest;
import com.google.cloud.kms.v1.ListKeyRingsRequest;
import com.google.cloud.secretmanager.v1.AccessSecretVersionRequest;
import com.google.cloud.secretmanager.v1.AddSecretVersionRequest;
import com.google.cloud.secretmanager.v1.CreateSecretRequest;
import com.google.cloud.secretmanager.v1.DeleteSecretRequest;
import com.google.cloud.secretmanager.v1.DestroySecretVersionRequest;
import com.google.cloud.secretmanager.v1.DisableSecretVersionRequest;
import com.google.cloud.secretmanager.v1.EnableSecretVersionRequest;
import com.google.cloud.secretmanager.v1.GetSecretRequest;
import com.google.cloud.secretmanager.v1.GetSecretVersionRequest;
import com.google.cloud.secretmanager.v1.ListSecretVersionsRequest;
import com.google.cloud.secretmanager.v1.ListSecretsRequest;
import com.google.cloud.secretmanager.v1.Secret;
import com.google.cloud.secretmanager.v1.SecretManagerServiceClient;
import com.google.cloud.secretmanager.v1.SecretManagerServiceSettings;
import com.google.cloud.secretmanager.v1.SecretPayload;
import com.google.cloud.secretmanager.v1.UpdateSecretRequest;
import com.google.protobuf.ByteString;
import com.google.protobuf.FieldMask;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import org.junit.jupiter.api.Test;

import java.security.MessageDigest;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

class GcpServiceLifecycleCompatibilityTest {
    private static final String PROJECT = "fcp-local";

    @Test
    void secretManagerKmsAndIamCredentialsUseOfficialClients() throws Exception {
        String endpoint = required("FCP_GCP_ENDPOINT");
        ManagedChannel channel = ManagedChannelBuilder.forTarget(endpoint).usePlaintext().build();
        var transport = FixedTransportChannelProvider.create(GrpcTransportChannel.create(channel));
        var credentials = NoCredentialsProvider.create();
        String suffix = Long.toUnsignedString(System.nanoTime());

        try {
            verifySecretManager(transport, credentials, suffix);
            verifyKms(transport, credentials, suffix);
            verifyIamCredentials(transport, credentials, suffix);
        } finally {
            channel.shutdownNow();
        }
    }

    private static void verifySecretManager(
            FixedTransportChannelProvider transport,
            NoCredentialsProvider credentials,
            String suffix
    ) throws Exception {
        var settings = SecretManagerServiceSettings.newBuilder()
                .setTransportChannelProvider(transport)
                .setCredentialsProvider(credentials)
                .build();
        try (SecretManagerServiceClient client = SecretManagerServiceClient.create(settings)) {
            String parent = "projects/" + PROJECT;
            String secretId = "java-lifecycle-" + suffix;
            Secret created = client.createSecret(CreateSecretRequest.newBuilder()
                    .setParent(parent)
                    .setSecretId(secretId)
                    .setSecret(Secret.newBuilder().putLabels("env", "test").build())
                    .build());
            assertEquals("test", client.getSecret(GetSecretRequest.newBuilder().setName(created.getName()).build())
                    .getLabelsOrThrow("env"));
            assertTrue(client.listSecrets(ListSecretsRequest.newBuilder().setParent(parent).setPageSize(1).build())
                    .iterateAll().iterator().hasNext());

            Secret updated = client.updateSecret(UpdateSecretRequest.newBuilder()
                    .setSecret(Secret.newBuilder().setName(created.getName()).putLabels("env", "updated").build())
                    .setUpdateMask(FieldMask.newBuilder().addPaths("labels").build())
                    .build());
            assertEquals("updated", updated.getLabelsOrThrow("env"));

            var first = client.addSecretVersion(AddSecretVersionRequest.newBuilder()
                    .setParent(created.getName())
                    .setPayload(SecretPayload.newBuilder().setData(ByteString.copyFromUtf8("first")).build())
                    .build());
            var second = client.addSecretVersion(AddSecretVersionRequest.newBuilder()
                    .setParent(created.getName())
                    .setPayload(SecretPayload.newBuilder().setData(ByteString.copyFromUtf8("second")).build())
                    .build());
            assertEquals(first.getName(), client.getSecretVersion(GetSecretVersionRequest.newBuilder()
                    .setName(first.getName()).build()).getName());
            assertTrue(client.listSecretVersions(ListSecretVersionsRequest.newBuilder()
                    .setParent(created.getName()).setPageSize(1).build()).iterateAll().iterator().hasNext());
            assertEquals("second", client.accessSecretVersion(AccessSecretVersionRequest.newBuilder()
                    .setName(created.getName() + "/versions/latest").build()).getPayload().getData().toStringUtf8());

            assertEquals(
                    com.google.cloud.secretmanager.v1.SecretVersion.State.DISABLED,
                    client.disableSecretVersion(DisableSecretVersionRequest.newBuilder().setName(second.getName()).build()).getState()
            );
            assertEquals(
                    com.google.cloud.secretmanager.v1.SecretVersion.State.ENABLED,
                    client.enableSecretVersion(EnableSecretVersionRequest.newBuilder().setName(second.getName()).build()).getState()
            );
            assertEquals(
                    com.google.cloud.secretmanager.v1.SecretVersion.State.DESTROYED,
                    client.destroySecretVersion(DestroySecretVersionRequest.newBuilder().setName(first.getName()).build()).getState()
            );
            client.deleteSecret(DeleteSecretRequest.newBuilder().setName(created.getName()).build());
        }
    }

    private static void verifyKms(
            FixedTransportChannelProvider transport,
            NoCredentialsProvider credentials,
            String suffix
    ) throws Exception {
        var settings = KeyManagementServiceSettings.newBuilder()
                .setTransportChannelProvider(transport)
                .setCredentialsProvider(credentials)
                .build();
        try (KeyManagementServiceClient client = KeyManagementServiceClient.create(settings)) {
            String location = "projects/" + PROJECT + "/locations/global";
            KeyRing ring = client.createKeyRing(CreateKeyRingRequest.newBuilder()
                    .setParent(location).setKeyRingId("java-ring-" + suffix).setKeyRing(KeyRing.newBuilder()).build());
            assertEquals(ring.getName(), client.getKeyRing(GetKeyRingRequest.newBuilder().setName(ring.getName()).build()).getName());
            assertTrue(client.listKeyRings(ListKeyRingsRequest.newBuilder().setParent(location).build())
                    .iterateAll().iterator().hasNext());

            CryptoKey symmetric = client.createCryptoKey(CreateCryptoKeyRequest.newBuilder()
                    .setParent(ring.getName())
                    .setCryptoKeyId("symmetric")
                    .setCryptoKey(CryptoKey.newBuilder().setPurpose(CryptoKey.CryptoKeyPurpose.ENCRYPT_DECRYPT).build())
                    .build());
            assertEquals(symmetric.getName(), client.getCryptoKey(GetCryptoKeyRequest.newBuilder()
                    .setName(symmetric.getName()).build()).getName());
            assertTrue(client.listCryptoKeys(ListCryptoKeysRequest.newBuilder().setParent(ring.getName()).build())
                    .iterateAll().iterator().hasNext());

            CryptoKeyVersion secondVersion = client.createCryptoKeyVersion(CreateCryptoKeyVersionRequest.newBuilder()
                    .setParent(symmetric.getName())
                    .setCryptoKeyVersion(CryptoKeyVersion.newBuilder()
                            .setAlgorithm(CryptoKeyVersion.CryptoKeyVersionAlgorithm.GOOGLE_SYMMETRIC_ENCRYPTION)
                            .build())
                    .build());
            assertEquals(secondVersion.getName(), client.getCryptoKeyVersion(GetCryptoKeyVersionRequest.newBuilder()
                    .setName(secondVersion.getName()).build()).getName());
            assertTrue(client.listCryptoKeyVersions(ListCryptoKeyVersionsRequest.newBuilder()
                    .setParent(symmetric.getName()).build()).iterateAll().iterator().hasNext());

            ByteString plaintext = ByteString.copyFromUtf8("official java kms");
            var encrypted = client.encrypt(EncryptRequest.newBuilder()
                    .setName(symmetric.getName()).setPlaintext(plaintext).build());
            var decrypted = client.decrypt(DecryptRequest.newBuilder()
                    .setName(symmetric.getName()).setCiphertext(encrypted.getCiphertext()).build());
            assertEquals(plaintext, decrypted.getPlaintext());

            CryptoKey signing = client.createCryptoKey(CreateCryptoKeyRequest.newBuilder()
                    .setParent(ring.getName())
                    .setCryptoKeyId("signing")
                    .setCryptoKey(CryptoKey.newBuilder()
                            .setPurpose(CryptoKey.CryptoKeyPurpose.ASYMMETRIC_SIGN)
                            .setVersionTemplate(CryptoKeyVersionTemplate.newBuilder()
                                    .setAlgorithm(CryptoKeyVersion.CryptoKeyVersionAlgorithm.RSA_SIGN_PKCS1_2048_SHA256)
                                    .build())
                            .build())
                    .build());
            String signingVersion = signing.getPrimary().getName();
            assertTrue(client.getPublicKey(GetPublicKeyRequest.newBuilder().setName(signingVersion).build())
                    .getPem().contains("BEGIN PUBLIC KEY"));
            byte[] digest = MessageDigest.getInstance("SHA-256").digest("sign me".getBytes());
            var signature = client.asymmetricSign(AsymmetricSignRequest.newBuilder()
                    .setName(signingVersion)
                    .setDigest(Digest.newBuilder().setSha256(ByteString.copyFrom(digest)).build())
                    .build());
            assertFalse(signature.getSignature().isEmpty());
        }
    }

    private static void verifyIamCredentials(
            FixedTransportChannelProvider transport,
            NoCredentialsProvider credentials,
            String suffix
    ) throws Exception {
        var settings = IamCredentialsSettings.newBuilder()
                .setTransportChannelProvider(transport)
                .setCredentialsProvider(credentials)
                .build();
        try (IamCredentialsClient client = IamCredentialsClient.create(settings)) {
            String name = "projects/-/serviceAccounts/java-" + suffix + "@" + PROJECT + ".iam.gserviceaccount.com";
            var access = client.generateAccessToken(GenerateAccessTokenRequest.newBuilder()
                    .setName(name).addAllScope(List.of("scope-one", "scope-two")).build());
            assertEquals(3, access.getAccessToken().split("\\.").length);
            assertNotNull(access.getExpireTime());

            var identity = client.generateIdToken(GenerateIdTokenRequest.newBuilder()
                    .setName(name).setAudience("fcp-test").setIncludeEmail(true).build());
            assertEquals(3, identity.getToken().split("\\.").length);

            var blob = client.signBlob(SignBlobRequest.newBuilder()
                    .setName(name).setPayload(ByteString.copyFromUtf8("payload")).build());
            assertFalse(blob.getSignedBlob().isEmpty());
            assertFalse(blob.getKeyId().isBlank());

            var jwt = client.signJwt(SignJwtRequest.newBuilder()
                    .setName(name).setPayload("{\"sub\":\"java-client\"}").build());
            assertEquals(3, jwt.getSignedJwt().split("\\.").length);
            assertFalse(jwt.getKeyId().isBlank());
        }
    }

    private static String required(String name) {
        String value = System.getenv(name);
        if (value == null || value.isBlank()) {
            throw new IllegalStateException(name + " is required");
        }
        return value;
    }
}
