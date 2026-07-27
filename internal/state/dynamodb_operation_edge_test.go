package state

import (
	"errors"
	"testing"
)

func TestDynamoOperationErrorsConditionsAndTransactions(t *testing.T) {
	store := openEdgeStore(t)
	pk, other, changed := "one", "other", "changed"
	keySchema := []DynamoKeySchemaElement{{AttributeName: "pk", KeyType: "HASH"}}
	definitions := []DynamoAttributeDefinition{{AttributeName: "pk", AttributeType: "S"}}
	item := DynamoItem{"pk": {S: &pk}, "value": {S: &other}}
	key := DynamoItem{"pk": {S: &pk}}

	if _, err := store.CreateDynamoTable("x", keySchema, definitions, ""); !errors.Is(err, ErrDynamoValidation) {
		t.Fatalf("invalid table creation error=%v", err)
	}
	table, err := store.CreateDynamoTable("items", keySchema, definitions, "")
	if err != nil || table.BillingMode != "PROVISIONED" {
		t.Fatalf("unexpected created table: %+v err=%v", table, err)
	}
	if _, err := store.CreateDynamoTable("items", keySchema, definitions, ""); !errors.Is(err, ErrDynamoTableExists) {
		t.Fatalf("duplicate table creation error=%v", err)
	}

	if _, _, err := store.DynamoGetItem("missing", key); !errors.Is(err, ErrDynamoTableNotFound) {
		t.Fatalf("missing table get error=%v", err)
	}
	if _, err := store.DynamoListItems("missing"); !errors.Is(err, ErrDynamoTableNotFound) {
		t.Fatalf("missing table list error=%v", err)
	}
	if _, _, err := store.DynamoPutItem("missing", item, nil); !errors.Is(err, ErrDynamoTableNotFound) {
		t.Fatalf("missing table put error=%v", err)
	}
	if _, _, err := store.DynamoDeleteItem("missing", key, nil); !errors.Is(err, ErrDynamoTableNotFound) {
		t.Fatalf("missing table delete item error=%v", err)
	}
	if _, _, err := store.DynamoUpdateItem("missing", key, nil, func(item DynamoItem) (DynamoItem, error) { return item, nil }); !errors.Is(err, ErrDynamoTableNotFound) {
		t.Fatalf("missing table update error=%v", err)
	}
	if _, _, err := store.DynamoPutItem("items", DynamoItem{"value": {S: &other}}, nil); !errors.Is(err, ErrDynamoValidation) {
		t.Fatalf("missing put key error=%v", err)
	}

	deny := func(DynamoItem, bool) bool { return false }
	if _, _, err := store.DynamoPutItem("items", item, deny); !errors.Is(err, ErrDynamoConditionalCheckFailed) {
		t.Fatalf("put condition error=%v", err)
	}
	if _, _, err := store.DynamoPutItem("items", item, nil); err != nil {
		t.Fatal(err)
	}
	old, existed, err := store.DynamoPutItem("items", DynamoItem{"pk": {S: &pk}, "value": {S: &changed}}, nil)
	if err != nil || !existed || old["value"].S == nil || *old["value"].S != other {
		t.Fatalf("overwrite result: old=%+v existed=%v err=%v", old, existed, err)
	}
	if _, _, err := store.DynamoDeleteItem("items", key, deny); !errors.Is(err, ErrDynamoConditionalCheckFailed) {
		t.Fatalf("delete condition error=%v", err)
	}
	missingKey := DynamoItem{"pk": {S: &other}}
	if old, existed, err := store.DynamoDeleteItem("items", missingKey, nil); err != nil || existed || old != nil {
		t.Fatalf("missing item delete result: old=%+v existed=%v err=%v", old, existed, err)
	}

	if _, _, err := store.DynamoUpdateItem("items", key, deny, func(item DynamoItem) (DynamoItem, error) { return item, nil }); !errors.Is(err, ErrDynamoConditionalCheckFailed) {
		t.Fatalf("update condition error=%v", err)
	}
	updateFailure := errors.New("update failed")
	if _, _, err := store.DynamoUpdateItem("items", key, nil, func(DynamoItem) (DynamoItem, error) { return nil, updateFailure }); !errors.Is(err, updateFailure) {
		t.Fatalf("update callback error=%v", err)
	}
	if _, _, err := store.DynamoUpdateItem("items", key, nil, func(item DynamoItem) (DynamoItem, error) {
		item["pk"] = DynamoAttributeValue{S: &other}
		return item, nil
	}); !errors.Is(err, ErrDynamoValidation) {
		t.Fatalf("primary key mutation error=%v", err)
	}
	if _, _, err := store.DynamoUpdateItem("items", missingKey, nil, func(item DynamoItem) (DynamoItem, error) {
		item["value"] = DynamoAttributeValue{S: &other}
		return item, nil
	}); err != nil {
		t.Fatalf("upsert update failed: %v", err)
	}

	if err := store.DynamoTransactWrite([]DynamoWriteOperation{{Kind: "bad", Table: "items", Item: item}}); !errors.Is(err, ErrDynamoValidation) {
		t.Fatalf("unsupported transaction operation error=%v", err)
	}
	if err := store.DynamoTransactWrite([]DynamoWriteOperation{{Kind: "put", Table: "missing", Item: item}}); !errors.Is(err, ErrDynamoTableNotFound) {
		t.Fatalf("missing transaction table error=%v", err)
	}
	if err := store.DynamoTransactWrite([]DynamoWriteOperation{{Kind: "put", Table: "items", Item: DynamoItem{}}}); !errors.Is(err, ErrDynamoValidation) {
		t.Fatalf("missing transaction key error=%v", err)
	}
	if err := store.DynamoTransactWrite([]DynamoWriteOperation{
		{Kind: "put", Table: "items", Item: item},
		{Kind: "delete", Table: "items", Key: key},
	}); !errors.Is(err, ErrDynamoValidation) {
		t.Fatalf("duplicate transaction item error=%v", err)
	}
	if err := store.DynamoTransactWrite([]DynamoWriteOperation{{Kind: "DELETE", Table: "items", Key: key}}); err != nil {
		t.Fatalf("valid delete transaction failed: %v", err)
	}

	if err := store.ClearDynamoTable("missing"); !errors.Is(err, ErrDynamoTableNotFound) {
		t.Fatalf("missing clear table error=%v", err)
	}
	if err := store.DeleteDynamoTable("missing"); !errors.Is(err, ErrDynamoTableNotFound) {
		t.Fatalf("missing delete table error=%v", err)
	}
	if err := store.DeleteDynamoTable("items"); err != nil {
		t.Fatal(err)
	}
}
