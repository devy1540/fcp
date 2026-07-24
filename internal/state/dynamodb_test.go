package state

import (
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

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
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
