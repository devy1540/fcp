package fcp

import com.google.api.gax.core.NoCredentialsProvider
import com.google.api.gax.grpc.GrpcTransportChannel
import com.google.api.gax.rpc.FixedTransportChannelProvider
import com.google.cloud.firestore.FirestoreOptions
import com.google.cloud.secretmanager.v1.AccessSecretVersionRequest
import com.google.cloud.secretmanager.v1.AddSecretVersionRequest
import com.google.cloud.secretmanager.v1.CreateSecretRequest
import com.google.cloud.secretmanager.v1.Secret
import com.google.cloud.secretmanager.v1.SecretManagerServiceClient
import com.google.cloud.secretmanager.v1.SecretManagerServiceSettings
import com.google.cloud.secretmanager.v1.SecretPayload
import com.google.protobuf.ByteString
import io.grpc.ManagedChannelBuilder
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Test

class KotlinSdkCompatibilityTest {
    @Test
    fun `Firestore and Secret Manager work from Kotlin`() {
        val endpoint = requireNotNull(System.getenv("FCP_GCP_ENDPOINT"))
        val firestoreOptions = FirestoreOptions.newBuilder()
            .setProjectId("fcp-local").setEmulatorHost(endpoint).build()
        assertEquals(endpoint, firestoreOptions.emulatorHost)
        val firestore = firestoreOptions.service
        try {
            val ref = firestore.collection("notifications").document("APP#kotlin#CHECK")
            ref.set(mapOf("reservationId" to 42L, "status" to "READY")).get()
            assertEquals("READY", ref.get().get().getString("status"))
            assertEquals(1, firestore.collection("notifications").whereEqualTo("reservationId", 42L).get().get().size())
        } finally {
            firestore.close()
        }

        val channel = ManagedChannelBuilder.forTarget(endpoint).usePlaintext().build()
        val settings = SecretManagerServiceSettings.newBuilder()
            .setTransportChannelProvider(FixedTransportChannelProvider.create(GrpcTransportChannel.create(channel)))
            .setCredentialsProvider(NoCredentialsProvider.create()).build()
        SecretManagerServiceClient.create(settings).use { client ->
            val parent = "projects/fcp-local"
			val secret = client.createSecret(CreateSecretRequest.newBuilder()
				.setParent(parent).setSecretId("kotlin-secret-${System.nanoTime()}").setSecret(Secret.newBuilder().build()).build())
            client.addSecretVersion(AddSecretVersionRequest.newBuilder().setParent(secret.name)
                .setPayload(SecretPayload.newBuilder().setData(ByteString.copyFromUtf8("kotlin-value"))).build())
            val accessed = client.accessSecretVersion(AccessSecretVersionRequest.newBuilder()
                .setName("${secret.name}/versions/latest").build())
            assertEquals("kotlin-value", accessed.payload.data.toStringUtf8())
        }
        channel.shutdownNow()
    }
}
