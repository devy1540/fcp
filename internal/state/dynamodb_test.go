package state

import (
	"errors"
	"testing"
)

func TestDynamoDBPersistsAndWorkloadResetPreservesTable(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDynamoTable("notifications",
		[]DynamoKeySchemaElement{{AttributeName: "pk", KeyType: "HASH"}, {AttributeName: "sk", KeyType: "RANGE"}},
		[]DynamoAttributeDefinition{{AttributeName: "pk", AttributeType: "S"}, {AttributeName: "sk", AttributeType: "S"}},
		"PAY_PER_REQUEST"); err != nil {
		t.Fatal(err)
	}
	pk, sk, value := "APP#1", "CHECK", "stored"
	item := DynamoItem{"pk": {S: &pk}, "sk": {S: &sk}, "value": {S: &value}}
	if _, _, err := store.DynamoPutItem("notifications", item, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, exists, err := reopened.DynamoGetItem("notifications", DynamoItem{"pk": {S: &pk}, "sk": {S: &sk}})
	if err != nil || !exists || stored["value"].S == nil || *stored["value"].S != "stored" {
		t.Fatalf("DynamoDB item did not persist: item=%+v exists=%v err=%v", stored, exists, err)
	}
	if err := reopened.ResetWorkloadData(); err != nil {
		t.Fatal(err)
	}
	table, err := reopened.DynamoTable("notifications")
	if err != nil || len(table.Items) != 0 {
		t.Fatalf("workload reset did not preserve empty table: table=%+v err=%v", table, err)
	}
}

func TestDynamoDBStateLifecycleAndTransactionAtomicity(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	schema := []DynamoKeySchemaElement{{AttributeName: "pk", KeyType: "HASH"}, {AttributeName: "sk", KeyType: "RANGE"}}
	definitions := []DynamoAttributeDefinition{{AttributeName: "pk", AttributeType: "S"}, {AttributeName: "sk", AttributeType: "S"}}
	if _, err := store.CreateDynamoTable("records", schema, definitions, "PAY_PER_REQUEST"); err != nil {
		t.Fatal(err)
	}
	if tables := store.ListDynamoTables(); len(tables) != 1 || tables[0].Name != "records" {
		t.Fatalf("unexpected table list: %+v", tables)
	}

	item := dynamoTestItem("A", "1", "created")
	if old, existed, err := store.DynamoPutItem("records", item, nil); err != nil || existed || old != nil {
		t.Fatalf("unexpected put: old=%+v existed=%v err=%v", old, existed, err)
	}
	items, err := store.DynamoListItems("records")
	if err != nil || len(items) != 1 || dynamoTestString(items[0], "status") != "created" {
		t.Fatalf("unexpected item list: items=%+v err=%v", items, err)
	}
	key := dynamoTestKey("A", "1")
	old, updated, err := store.DynamoUpdateItem("records", key, func(_ DynamoItem, exists bool) bool {
		return exists
	}, func(current DynamoItem) (DynamoItem, error) {
		next := cloneDynamoItem(current)
		status := "updated"
		next["status"] = DynamoAttributeValue{S: &status}
		return next, nil
	})
	if err != nil || dynamoTestString(old, "status") != "created" || dynamoTestString(updated, "status") != "updated" {
		t.Fatalf("unexpected update: old=%+v updated=%+v err=%v", old, updated, err)
	}
	if _, _, err := store.DynamoUpdateItem("records", key, nil, func(current DynamoItem) (DynamoItem, error) {
		next := cloneDynamoItem(current)
		changed := "B"
		next["pk"] = DynamoAttributeValue{S: &changed}
		return next, nil
	}); !errors.Is(err, ErrDynamoValidation) {
		t.Fatalf("primary key mutation should fail: %v", err)
	}
	if _, _, err := store.DynamoDeleteItem("records", key, func(_ DynamoItem, _ bool) bool { return false }); !errors.Is(err, ErrDynamoConditionalCheckFailed) {
		t.Fatalf("failed delete condition should be reported: %v", err)
	}

	second := dynamoTestItem("B", "1", "second")
	if err := store.DynamoTransactWrite([]DynamoWriteOperation{
		{Kind: "put", Table: "records", Item: second},
		{Kind: "delete", Table: "records", Key: key},
	}); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := store.DynamoGetItem("records", key); err != nil || exists {
		t.Fatalf("transaction delete failed: exists=%v err=%v", exists, err)
	}
	secondKey := dynamoTestKey("B", "1")
	if stored, exists, err := store.DynamoGetItem("records", secondKey); err != nil || !exists || dynamoTestString(stored, "status") != "second" {
		t.Fatalf("transaction put failed: item=%+v exists=%v err=%v", stored, exists, err)
	}

	third := dynamoTestItem("C", "1", "must-not-apply")
	if err := store.DynamoTransactWrite([]DynamoWriteOperation{
		{Kind: "put", Table: "records", Item: third},
		{Kind: "unsupported", Table: "records", Key: secondKey},
	}); !errors.Is(err, ErrDynamoValidation) {
		t.Fatalf("unsupported transaction should fail validation: %v", err)
	}
	if _, exists, err := store.DynamoGetItem("records", dynamoTestKey("C", "1")); err != nil || exists {
		t.Fatalf("failed transaction leaked a mutation: exists=%v err=%v", exists, err)
	}
	if _, _, err := store.DynamoDeleteItem("records", secondKey, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearDynamoTable("records"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDynamoTable("records"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDynamoTable("records"); !errors.Is(err, ErrDynamoTableNotFound) {
		t.Fatalf("second delete should report not found: %v", err)
	}
}

func dynamoTestItem(pk, sk, status string) DynamoItem {
	return DynamoItem{
		"pk": {S: &pk}, "sk": {S: &sk}, "status": {S: &status},
	}
}

func dynamoTestKey(pk, sk string) DynamoItem {
	return DynamoItem{"pk": {S: &pk}, "sk": {S: &sk}}
}

func dynamoTestString(item DynamoItem, name string) string {
	if item[name].S == nil {
		return ""
	}
	return *item[name].S
}
