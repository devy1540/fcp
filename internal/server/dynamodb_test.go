package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestDynamoDBPodoCoreLifecycle(t *testing.T) {
	server := newTestServer(t)
	create := map[string]any{
		"TableName": "podo-notification",
		"KeySchema": []map[string]string{
			{"AttributeName": "pk", "KeyType": "HASH"},
			{"AttributeName": "sk", "KeyType": "RANGE"},
		},
		"AttributeDefinitions": []map[string]string{
			{"AttributeName": "pk", "AttributeType": "S"},
			{"AttributeName": "sk", "AttributeType": "S"},
		},
		"BillingMode": "PAY_PER_REQUEST",
	}
	created := dynamoCall(t, server.URL, "CreateTable", create, http.StatusOK)
	if created["TableDescription"].(map[string]any)["TableStatus"] != "ACTIVE" {
		t.Fatalf("unexpected CreateTable response: %+v", created)
	}
	duplicate := dynamoCallResponse(t, server.URL, "CreateTable", create)
	if duplicate.StatusCode != http.StatusBadRequest || duplicate.Header.Get("x-amzn-ErrorType") != "ResourceInUseException" {
		t.Fatalf("duplicate table status=%d error=%q", duplicate.StatusCode, duplicate.Header.Get("x-amzn-ErrorType"))
	}
	duplicate.Body.Close()

	putDynamoTestItem(t, server.URL, "APP#one", "Alpha", "dev", 1)
	putDynamoTestItem(t, server.URL, "APP#one", "Beta", "dev", 2)
	putDynamoTestItem(t, server.URL, "APP#two", "Alpha", "prod", 5)

	get := dynamoCall(t, server.URL, "GetItem", map[string]any{
		"TableName": "podo-notification",
		"Key":       dynamoStringItem(map[string]string{"pk": "APP#one", "sk": "Alpha"}),
	}, http.StatusOK)
	item := get["Item"].(map[string]any)
	if item["env"].(map[string]any)["S"] != "dev" {
		t.Fatalf("unexpected GetItem: %+v", get)
	}

	query := dynamoCall(t, server.URL, "Query", map[string]any{
		"TableName":              "podo-notification",
		"KeyConditionExpression": "#pk = :pk AND begins_with(#sk, :prefix)",
		"ExpressionAttributeNames": map[string]string{
			"#pk": "pk", "#sk": "sk",
		},
		"ExpressionAttributeValues": map[string]any{
			":pk": map[string]string{"S": "APP#one"}, ":prefix": map[string]string{"S": "A"},
		},
	}, http.StatusOK)
	if query["Count"].(float64) != 1 || query["ScannedCount"].(float64) != 1 {
		t.Fatalf("unexpected Query response: %+v", query)
	}

	filtered := dynamoCall(t, server.URL, "Query", map[string]any{
		"TableName":              "podo-notification",
		"KeyConditionExpression": "pk = :pk",
		"FilterExpression":       "#env = :env AND #count >= :minimum",
		"ExpressionAttributeNames": map[string]string{
			"#env": "env", "#count": "count",
		},
		"ExpressionAttributeValues": map[string]any{
			":pk": map[string]string{"S": "APP#one"}, ":env": map[string]string{"S": "dev"}, ":minimum": map[string]string{"N": "2"},
		},
	}, http.StatusOK)
	if filtered["Count"].(float64) != 1 || filtered["ScannedCount"].(float64) != 2 {
		t.Fatalf("unexpected filtered Query response: %+v", filtered)
	}

	updated := dynamoCall(t, server.URL, "UpdateItem", map[string]any{
		"TableName":        "podo-notification",
		"Key":              dynamoStringItem(map[string]string{"pk": "APP#one", "sk": "Alpha"}),
		"UpdateExpression": "SET #status = :status, #created = if_not_exists(#created, :created) ADD #count :increment REMOVE #env",
		"ExpressionAttributeNames": map[string]string{
			"#status": "status", "#created": "created", "#count": "count", "#env": "env",
		},
		"ExpressionAttributeValues": map[string]any{
			":status": map[string]string{"S": "SENT"}, ":created": map[string]string{"S": "now"}, ":increment": map[string]string{"N": "4"},
		},
		"ReturnValues": "ALL_NEW",
	}, http.StatusOK)
	updatedItem := updated["Attributes"].(map[string]any)
	if updatedItem["count"].(map[string]any)["N"] != "5" || updatedItem["status"].(map[string]any)["S"] != "SENT" {
		t.Fatalf("unexpected UpdateItem response: %+v", updated)
	}
	if _, exists := updatedItem["env"]; exists {
		t.Fatalf("REMOVE did not delete env: %+v", updatedItem)
	}

	conditional := dynamoCallResponse(t, server.URL, "UpdateItem", map[string]any{
		"TableName":                "podo-notification",
		"Key":                      dynamoStringItem(map[string]string{"pk": "APP#one", "sk": "Alpha"}),
		"UpdateExpression":         "SET #status = :next",
		"ConditionExpression":      "#status = :expected",
		"ExpressionAttributeNames": map[string]string{"#status": "status"},
		"ExpressionAttributeValues": map[string]any{
			":next": map[string]string{"S": "DONE"}, ":expected": map[string]string{"S": "PENDING"},
		},
	})
	if conditional.StatusCode != http.StatusBadRequest || conditional.Header.Get("x-amzn-ErrorType") != "ConditionalCheckFailedException" {
		t.Fatalf("condition status=%d error=%q", conditional.StatusCode, conditional.Header.Get("x-amzn-ErrorType"))
	}
	conditional.Body.Close()

	scan := dynamoCall(t, server.URL, "Scan", map[string]any{
		"TableName":                 "podo-notification",
		"FilterExpression":          "#env = :env",
		"ExpressionAttributeNames":  map[string]string{"#env": "env"},
		"ExpressionAttributeValues": map[string]any{":env": map[string]string{"S": "prod"}},
	}, http.StatusOK)
	if scan["Count"].(float64) != 1 || scan["ScannedCount"].(float64) != 3 {
		t.Fatalf("unexpected Scan response: %+v", scan)
	}

	batchGet := dynamoCall(t, server.URL, "BatchGetItem", map[string]any{
		"RequestItems": map[string]any{
			"podo-notification": map[string]any{
				"Keys": []any{
					dynamoStringItem(map[string]string{"pk": "APP#one", "sk": "Alpha"}),
					dynamoStringItem(map[string]string{"pk": "APP#two", "sk": "Alpha"}),
					dynamoStringItem(map[string]string{"pk": "APP#missing", "sk": "CHECK"}),
				},
				"ProjectionExpression":     "#pk, #sk",
				"ExpressionAttributeNames": map[string]string{"#pk": "pk", "#sk": "sk"},
			},
		},
	}, http.StatusOK)
	batchItems := batchGet["Responses"].(map[string]any)["podo-notification"].([]any)
	if len(batchItems) != 2 || len(batchGet["UnprocessedKeys"].(map[string]any)) != 0 {
		t.Fatalf("unexpected BatchGetItem response: %+v", batchGet)
	}

	batchWrite := dynamoCall(t, server.URL, "BatchWriteItem", map[string]any{
		"RequestItems": map[string]any{
			"podo-notification": []any{
				map[string]any{"PutRequest": map[string]any{"Item": dynamoStringItem(map[string]string{"pk": "APP#batch", "sk": "CHECK"})}},
				map[string]any{"DeleteRequest": map[string]any{"Key": dynamoStringItem(map[string]string{"pk": "APP#two", "sk": "Alpha"})}},
			},
		},
	}, http.StatusOK)
	if len(batchWrite["UnprocessedItems"].(map[string]any)) != 0 {
		t.Fatalf("unexpected BatchWriteItem response: %+v", batchWrite)
	}
	deletedByBatch := dynamoCall(t, server.URL, "GetItem", map[string]any{
		"TableName": "podo-notification",
		"Key":       dynamoStringItem(map[string]string{"pk": "APP#two", "sk": "Alpha"}),
	}, http.StatusOK)
	if _, exists := deletedByBatch["Item"]; exists {
		t.Fatalf("BatchWriteItem did not delete item: %+v", deletedByBatch)
	}
	createdByBatch := dynamoCall(t, server.URL, "GetItem", map[string]any{
		"TableName": "podo-notification",
		"Key":       dynamoStringItem(map[string]string{"pk": "APP#batch", "sk": "CHECK"}),
	}, http.StatusOK)
	if _, exists := createdByBatch["Item"]; !exists {
		t.Fatalf("BatchWriteItem did not put item: %+v", createdByBatch)
	}

	dynamoCall(t, server.URL, "TransactWriteItems", map[string]any{
		"TransactItems": []any{
			map[string]any{"Delete": map[string]any{"TableName": "podo-notification", "Key": dynamoStringItem(map[string]string{"pk": "APP#one", "sk": "Beta"})}},
			map[string]any{"Put": map[string]any{"TableName": "podo-notification", "Item": dynamoStringItem(map[string]string{"pk": "APP#three", "sk": "CHECK"})}},
		},
	}, http.StatusOK)

	described := dynamoCall(t, server.URL, "DescribeTable", map[string]string{"TableName": "podo-notification"}, http.StatusOK)
	if described["Table"].(map[string]any)["ItemCount"].(float64) != 3 {
		t.Fatalf("unexpected item count after transaction: %+v", described)
	}
	listed := dynamoCall(t, server.URL, "ListTables", map[string]any{}, http.StatusOK)
	if len(listed["TableNames"].([]any)) != 1 {
		t.Fatalf("unexpected ListTables: %+v", listed)
	}
	dynamoCall(t, server.URL, "DeleteTable", map[string]string{"TableName": "podo-notification"}, http.StatusOK)
	notFound := dynamoCallResponse(t, server.URL, "DescribeTable", map[string]string{"TableName": "podo-notification"})
	if notFound.StatusCode != http.StatusBadRequest || notFound.Header.Get("x-amzn-ErrorType") != "ResourceNotFoundException" {
		t.Fatalf("deleted table status=%d error=%q", notFound.StatusCode, notFound.Header.Get("x-amzn-ErrorType"))
	}
	notFound.Body.Close()
}

func TestSTSGetCallerIdentity(t *testing.T) {
	server := newTestServer(t)
	form := url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}
	response, err := http.Post(server.URL+"/", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("<Account>000000000000</Account>")) || !bytes.Contains(body, []byte("arn:aws:iam::000000000000:user/fcp-local")) {
		t.Fatalf("unexpected STS response status=%d body=%s", response.StatusCode, body)
	}
}

func putDynamoTestItem(t *testing.T, endpoint, pk, sk, env string, count int) {
	t.Helper()
	dynamoCall(t, endpoint, "PutItem", map[string]any{
		"TableName": "podo-notification",
		"Item": map[string]any{
			"pk": map[string]string{"S": pk}, "sk": map[string]string{"S": sk}, "env": map[string]string{"S": env}, "count": map[string]string{"N": strconv.Itoa(count)},
		},
	}, http.StatusOK)
}

func dynamoStringItem(values map[string]string) map[string]any {
	item := map[string]any{}
	for key, value := range values {
		item[key] = map[string]string{"S": value}
	}
	return item
}

func dynamoCall(t *testing.T, endpoint, operation string, payload any, expectedStatus int) map[string]any {
	t.Helper()
	response := dynamoCallResponse(t, endpoint, operation, payload)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != expectedStatus {
		t.Fatalf("DynamoDB %s status=%d want=%d body=%s", operation, response.StatusCode, expectedStatus, body)
	}
	result := map[string]any{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode DynamoDB %s response: %v body=%s", operation, err, body)
	}
	return result
}

func dynamoCallResponse(t *testing.T, endpoint, operation string, payload any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, endpoint+"/", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-amz-json-1.0")
	request.Header.Set("X-Amz-Target", dynamoTargetPrefix+operation)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
