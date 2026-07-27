package fcp;

import com.google.api.gax.core.NoCredentialsProvider;
import com.google.api.gax.grpc.GrpcTransportChannel;
import com.google.api.gax.rpc.FixedTransportChannelProvider;
import com.google.cloud.firestore.v1.FirestoreClient;
import com.google.cloud.firestore.v1.FirestoreSettings;
import com.google.firestore.v1.BatchWriteRequest;
import com.google.firestore.v1.BeginTransactionRequest;
import com.google.firestore.v1.CreateDocumentRequest;
import com.google.firestore.v1.DeleteDocumentRequest;
import com.google.firestore.v1.Document;
import com.google.firestore.v1.DocumentMask;
import com.google.firestore.v1.GetDocumentRequest;
import com.google.firestore.v1.ListDocumentsRequest;
import com.google.firestore.v1.RollbackRequest;
import com.google.firestore.v1.UpdateDocumentRequest;
import com.google.firestore.v1.Value;
import com.google.firestore.v1.Write;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import org.junit.jupiter.api.Test;

import java.util.stream.StreamSupport;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class FirestoreGrpcLifecycleCompatibilityTest {
    private static final String PROJECT = "fcp-local";

    @Test
    void generatedFirestoreClientCoversDocumentAndTransactionLifecycle() throws Exception {
        String endpoint = required("FCP_GCP_ENDPOINT");
        ManagedChannel channel = ManagedChannelBuilder.forTarget(endpoint).usePlaintext().build();
        var settings = FirestoreSettings.newBuilder()
                .setTransportChannelProvider(FixedTransportChannelProvider.create(GrpcTransportChannel.create(channel)))
                .setCredentialsProvider(NoCredentialsProvider.create())
                .build();
        String suffix = Long.toUnsignedString(System.nanoTime());
        String database = "projects/" + PROJECT + "/databases/(default)";
        String parent = database + "/documents";
        String collection = "java-direct-" + suffix;

        try (FirestoreClient client = FirestoreClient.create(settings)) {
            Document first = client.createDocument(CreateDocumentRequest.newBuilder()
                    .setParent(parent)
                    .setCollectionId(collection)
                    .setDocumentId("first")
                    .setDocument(document("CREATED", 1))
                    .build());
            Document second = client.createDocument(CreateDocumentRequest.newBuilder()
                    .setParent(parent)
                    .setCollectionId(collection)
                    .setDocumentId("second")
                    .setDocument(document("CREATED", 2))
                    .build());

            assertEquals("CREATED", client.getDocument(GetDocumentRequest.newBuilder()
                    .setName(first.getName())
                    .setMask(DocumentMask.newBuilder().addFieldPaths("status").build())
                    .build()).getFieldsOrThrow("status").getStringValue());
            var listed = client.listDocuments(ListDocumentsRequest.newBuilder()
                    .setParent(parent)
                    .setCollectionId(collection)
                    .setPageSize(1)
                    .build());
            assertEquals(2, StreamSupport.stream(listed.iterateAll().spliterator(), false).count());

            Document updated = client.updateDocument(UpdateDocumentRequest.newBuilder()
                    .setDocument(Document.newBuilder()
                            .setName(first.getName())
                            .putFields("status", Value.newBuilder().setStringValue("UPDATED").build())
                            .build())
                    .setUpdateMask(DocumentMask.newBuilder().addFieldPaths("status").build())
                    .build());
            assertEquals("UPDATED", updated.getFieldsOrThrow("status").getStringValue());
            assertEquals(1, updated.getFieldsOrThrow("sequence").getIntegerValue());

            var batch = client.batchWrite(BatchWriteRequest.newBuilder()
                    .setDatabase(database)
                    .addWrites(Write.newBuilder()
                            .setUpdate(Document.newBuilder()
                                    .setName(first.getName())
                                    .putFields("status", Value.newBuilder().setStringValue("BATCHED").build())
                                    .putFields("sequence", Value.newBuilder().setIntegerValue(3).build())
                                    .build())
                            .build())
                    .addWrites(Write.newBuilder().setDelete(second.getName()).build())
                    .build());
            assertEquals(2, batch.getStatusCount());
            assertTrue(batch.getStatusList().stream().allMatch(status -> status.getCode() == 0));

            var transaction = client.beginTransaction(BeginTransactionRequest.newBuilder()
                    .setDatabase(database)
                    .build());
            assertFalse(transaction.getTransaction().isEmpty());
            client.rollback(RollbackRequest.newBuilder()
                    .setDatabase(database)
                    .setTransaction(transaction.getTransaction())
                    .build());

            client.deleteDocument(DeleteDocumentRequest.newBuilder().setName(first.getName()).build());
        } finally {
            channel.shutdownNow();
        }
    }

    private static Document document(String status, long sequence) {
        return Document.newBuilder()
                .putFields("status", Value.newBuilder().setStringValue(status).build())
                .putFields("sequence", Value.newBuilder().setIntegerValue(sequence).build())
                .build();
    }

    private static String required(String name) {
        String value = System.getenv(name);
        if (value == null || value.isBlank()) {
            throw new IllegalStateException(name + " is required");
        }
        return value;
    }
}
