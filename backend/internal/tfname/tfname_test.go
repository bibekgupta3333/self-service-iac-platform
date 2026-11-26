package tfname

import (
	"encoding/json"
	"testing"
)

func TestValidateInputName(t *testing.T) {
	for name, ok := range map[string]bool{
		"name": true, "visibility_timeout_seconds": true, "a": true,
		"Name": false, "bad-name": false, "1abc": false, "": false, "with space": false,
		"source": false, "count": false, "for_each": false, "depends_on": false, "providers": false, "version": false,
	} {
		if err := ValidateInputName(name); (err == nil) != ok {
			t.Errorf("ValidateInputName(%q) = %v, want ok=%v", name, err, ok)
		}
	}
}

func TestParseRef(t *testing.T) {
	tests := []struct {
		in      string
		isRef   bool
		wantErr bool
		want    Ref
	}{
		{in: `{"$ref":"orders_queue.queue_arn"}`, isRef: true, want: Ref{"orders_queue", "queue_arn"}},
		{in: `{"$ref":"orders_queue"}`, isRef: true, wantErr: true},
		{in: `{"$ref":"module.x.y"}`, isRef: true, wantErr: true},
		{in: `{"$ref":"Orders.arn"}`, isRef: true, wantErr: true},
		{in: `{"$ref":42}`, isRef: true, wantErr: true},
		{in: `{"$ref":"a.b","extra":1}`},
		{in: `{"name":"x"}`},
		{in: `"a.b"`},
		{in: `[{"$ref":"a.b"}]`},
	}
	for _, tt := range tests {
		var v any
		if err := json.Unmarshal([]byte(tt.in), &v); err != nil {
			t.Fatal(err)
		}
		got, isRef, err := ParseRef(v)
		if isRef != tt.isRef || (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseRef(%s) = %+v, %v, %v", tt.in, got, isRef, err)
		}
	}
	if (Ref{"a", "b"}).String() != "a.b" {
		t.Fatal("String")
	}
}
