package fcp;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;

import java.net.URI;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import software.amazon.awssdk.auth.credentials.AwsBasicCredentials;
import software.amazon.awssdk.auth.credentials.StaticCredentialsProvider;
import software.amazon.awssdk.enhanced.dynamodb.DynamoDbEnhancedClient;
import software.amazon.awssdk.enhanced.dynamodb.DynamoDbTable;
import software.amazon.awssdk.enhanced.dynamodb.Key;
import software.amazon.awssdk.enhanced.dynamodb.TableSchema;
import software.amazon.awssdk.enhanced.dynamodb.mapper.annotations.DynamoDbBean;
import software.amazon.awssdk.enhanced.dynamodb.mapper.annotations.DynamoDbPartitionKey;
import software.amazon.awssdk.enhanced.dynamodb.mapper.annotations.DynamoDbSortKey;
import software.amazon.awssdk.enhanced.dynamodb.model.QueryConditional;
import software.amazon.awssdk.regions.Region;
import software.amazon.awssdk.services.dynamodb.DynamoDbClient;
import software.amazon.awssdk.services.dynamodb.model.AttributeDefinition;
import software.amazon.awssdk.services.dynamodb.model.AttributeValue;
import software.amazon.awssdk.services.dynamodb.model.BatchGetItemRequest;
import software.amazon.awssdk.services.dynamodb.model.BatchWriteItemRequest;
import software.amazon.awssdk.services.dynamodb.model.BillingMode;
import software.amazon.awssdk.services.dynamodb.model.CreateTableRequest;
import software.amazon.awssdk.services.dynamodb.model.Delete;
import software.amazon.awssdk.services.dynamodb.model.DeleteRequest;
import software.amazon.awssdk.services.dynamodb.model.KeySchemaElement;
import software.amazon.awssdk.services.dynamodb.model.KeyType;
import software.amazon.awssdk.services.dynamodb.model.KeysAndAttributes;
import software.amazon.awssdk.services.dynamodb.model.PutRequest;
import software.amazon.awssdk.services.dynamodb.model.ScalarAttributeType;
import software.amazon.awssdk.services.dynamodb.model.TransactWriteItem;
import software.amazon.awssdk.services.dynamodb.model.TransactWriteItemsRequest;
import software.amazon.awssdk.services.dynamodb.model.UpdateItemRequest;
import software.amazon.awssdk.services.dynamodb.model.WriteRequest;
import software.amazon.awssdk.services.sqs.SqsAsyncClient;
import software.amazon.awssdk.services.sqs.model.DeleteMessageBatchRequestEntry;
import software.amazon.awssdk.services.sqs.model.MessageAttributeValue;
import software.amazon.awssdk.services.sts.StsClient;

class AwsSdkCompatibilityTest {

    @Test
    void demoNotificationSqsAsyncClientWorksWithFcp() {
        String endpoint = System.getenv().getOrDefault("FCP_HTTP_ENDPOINT", "http://127.0.0.1:4566");
        var credentials = StaticCredentialsProvider.create(AwsBasicCredentials.create("test", "test"));
        var sqsBuilder = SqsAsyncClient.builder()
                .region(Region.AP_NORTHEAST_2)
                .credentialsProvider(credentials);
        if (System.getenv("AWS_ENDPOINT_URL") == null) {
            sqsBuilder.endpointOverride(URI.create(endpoint));
        }

        try (var sqs = sqsBuilder.build()) {
            String queueUrl = sqs.getQueueUrl(builder -> builder.queueName("notifications")).join().queueUrl();
            assertNotNull(sqs.getQueueUrl(builder -> builder.queueName("scheduled-jobs")).join().queueUrl());
            sqs.purgeQueue(builder -> builder.queueUrl(queueUrl)).join();

            sqs.sendMessage(builder -> builder
                    .queueUrl(queueUrl)
                    .messageBody("{\"apiKey\":\"local\",\"payload\":{\"id\":1}}")
                    .messageAttributes(Map.of("contentType", MessageAttributeValue.builder()
                            .dataType("String")
                            .stringValue("application/json")
                            .build())))
                    .join();
            var received = sqs.receiveMessage(builder -> builder
                    .queueUrl(queueUrl)
                    .maxNumberOfMessages(10)
                    .waitTimeSeconds(1)
                    .visibilityTimeout(30))
                    .join();
            assertEquals(1, received.messages().size());
            assertEquals("application/json", received.messages().getFirst()
                    .messageAttributes().get("contentType").stringValue());

            var deleted = sqs.deleteMessageBatch(builder -> builder
                    .queueUrl(queueUrl)
                    .entries(DeleteMessageBatchRequestEntry.builder()
                            .id("0")
                            .receiptHandle(received.messages().getFirst().receiptHandle())
                            .build()))
                    .join();
            assertEquals(1, deleted.successful().size());
            assertEquals(0, deleted.failed().size());
        }
    }

    @Test
    void demoNotificationDynamoDbAndStsVersionsWorkWithFcp() {
        String endpoint = System.getenv().getOrDefault("FCP_HTTP_ENDPOINT", "http://127.0.0.1:4566");
        var credentials = StaticCredentialsProvider.create(AwsBasicCredentials.create("test", "test"));
        var endpointUri = URI.create(endpoint);
        String tableName = "notifications-sdk-" + System.nanoTime();
        var dynamoBuilder = DynamoDbClient.builder()
                .region(Region.AP_NORTHEAST_2)
                .credentialsProvider(credentials);
        var stsBuilder = StsClient.builder()
                .region(Region.AP_NORTHEAST_2)
                .credentialsProvider(credentials);
        if (System.getenv("AWS_ENDPOINT_URL") == null) {
            dynamoBuilder.endpointOverride(endpointUri);
            stsBuilder.endpointOverride(endpointUri);
        }

        try (var dynamo = dynamoBuilder.build();
             var sts = stsBuilder.build()) {
            dynamo.createTable(CreateTableRequest.builder()
                    .tableName(tableName)
                    .keySchema(
                            KeySchemaElement.builder().attributeName("pk").keyType(KeyType.HASH).build(),
                            KeySchemaElement.builder().attributeName("sk").keyType(KeyType.RANGE).build())
                    .attributeDefinitions(
                            AttributeDefinition.builder().attributeName("pk").attributeType(ScalarAttributeType.S).build(),
                            AttributeDefinition.builder().attributeName("sk").attributeType(ScalarAttributeType.S).build())
                    .billingMode(BillingMode.PAY_PER_REQUEST)
                    .build());
            assertEquals("ACTIVE", dynamo.describeTable(builder -> builder.tableName(tableName)).table().tableStatusAsString());

            DynamoDbTable<NotificationRecord> table = DynamoDbEnhancedClient.builder()
                    .dynamoDbClient(dynamo)
                    .build()
                    .table(tableName, TableSchema.fromBean(NotificationRecord.class));
            table.putItem(new NotificationRecord("APP#local", "RESERVATION#1", "PENDING", 1));
            table.putItem(new NotificationRecord("APP#local", "RESERVATION#2", "PENDING", 2));

            var batchRead = dynamo.batchGetItem(BatchGetItemRequest.builder()
                    .requestItems(Map.of(tableName, KeysAndAttributes.builder()
                            .keys(
                                    Map.of(
                                            "pk", AttributeValue.builder().s("APP#local").build(),
                                            "sk", AttributeValue.builder().s("RESERVATION#1").build()),
                                    Map.of(
                                            "pk", AttributeValue.builder().s("APP#local").build(),
                                            "sk", AttributeValue.builder().s("RESERVATION#2").build()))
                            .build()))
                    .build());
            assertEquals(2, batchRead.responses().get(tableName).size());
            assertEquals(0, batchRead.unprocessedKeys().size());

            var batchWrite = dynamo.batchWriteItem(BatchWriteItemRequest.builder()
                    .requestItems(Map.of(tableName, List.of(
                            WriteRequest.builder()
                                    .putRequest(PutRequest.builder().item(Map.of(
                                            "pk", AttributeValue.builder().s("APP#local").build(),
                                            "sk", AttributeValue.builder().s("RESERVATION#3").build(),
                                            "status", AttributeValue.builder().s("PENDING").build()))
                                            .build())
                                    .build(),
                            WriteRequest.builder()
                                    .deleteRequest(DeleteRequest.builder().key(Map.of(
                                            "pk", AttributeValue.builder().s("APP#local").build(),
                                            "sk", AttributeValue.builder().s("RESERVATION#2").build()))
                                            .build())
                                    .build())))
                    .build());
            assertEquals(0, batchWrite.unprocessedItems().size());
            assertNotNull(table.getItem(Key.builder()
                    .partitionValue("APP#local")
                    .sortValue("RESERVATION#3")
                    .build()));

            NotificationRecord stored = table.getItem(Key.builder()
                    .partitionValue("APP#local")
                    .sortValue("RESERVATION#1")
                    .build());
            assertNotNull(stored);
            assertEquals("PENDING", stored.getStatus());
            assertEquals(2, table.query(QueryConditional.keyEqualTo(
                    Key.builder().partitionValue("APP#local").build())).items().stream().count());
            assertEquals(2, table.query(QueryConditional.sortBeginsWith(
                    Key.builder().partitionValue("APP#local").sortValue("RESERVATION#").build())).items().stream().count());

            dynamo.updateItem(UpdateItemRequest.builder()
                    .tableName(tableName)
                    .key(Map.of(
                            "pk", AttributeValue.builder().s("APP#local").build(),
                            "sk", AttributeValue.builder().s("RESERVATION#1").build()))
                    .updateExpression("SET #status = :sent ADD #attempts :one")
                    .conditionExpression("#status = :pending")
                    .expressionAttributeNames(Map.of("#status", "status", "#attempts", "attempts"))
                    .expressionAttributeValues(Map.of(
                            ":sent", AttributeValue.builder().s("SENT").build(),
                            ":pending", AttributeValue.builder().s("PENDING").build(),
                            ":one", AttributeValue.builder().n("1").build()))
                    .build());
            assertEquals("SENT", table.getItem(Key.builder()
                    .partitionValue("APP#local")
                    .sortValue("RESERVATION#1")
                    .build()).getStatus());

            dynamo.transactWriteItems(TransactWriteItemsRequest.builder()
                    .transactItems(TransactWriteItem.builder()
                            .delete(Delete.builder()
                                    .tableName(tableName)
                                    .key(Map.of(
                                            "pk", AttributeValue.builder().s("APP#local").build(),
                                            "sk", AttributeValue.builder().s("RESERVATION#3").build()))
                                    .build())
                            .build())
                    .build());
            assertEquals(1L, table.query(QueryConditional.keyEqualTo(
                    Key.builder().partitionValue("APP#local").build())).items().stream().count());

            assertEquals("000000000000", sts.getCallerIdentity().account());
            assertEquals("arn:aws:iam::000000000000:user/fcp-local", sts.getCallerIdentity().arn());
            dynamo.deleteTable(builder -> builder.tableName(tableName));
        }
    }

    @DynamoDbBean
    public static class NotificationRecord {
        private String pk;
        private String sk;
        private String status;
        private Integer attempts;

        public NotificationRecord() {
        }

        NotificationRecord(String pk, String sk, String status, Integer attempts) {
            this.pk = pk;
            this.sk = sk;
            this.status = status;
            this.attempts = attempts;
        }

        @DynamoDbPartitionKey
        public String getPk() {
            return pk;
        }

        public void setPk(String pk) {
            this.pk = pk;
        }

        @DynamoDbSortKey
        public String getSk() {
            return sk;
        }

        public void setSk(String sk) {
            this.sk = sk;
        }

        public String getStatus() {
            return status;
        }

        public void setStatus(String status) {
            this.status = status;
        }

        public Integer getAttempts() {
            return attempts;
        }

        public void setAttempts(Integer attempts) {
            this.attempts = attempts;
        }
    }
}
