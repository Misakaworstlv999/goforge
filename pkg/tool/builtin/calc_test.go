package builtin

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCalculator_Operations(t *testing.T) {
	calc := NewCalculator()

	tests := []struct {
		name    string
		args    CalcArgs
		want    string
		wantErr bool
	}{
		{name: "add", args: CalcArgs{A: 3, B: 4, Op: "add"}, want: "7"},
		{name: "subtract", args: CalcArgs{A: 10, B: 3, Op: "subtract"}, want: "7"},
		{name: "multiply", args: CalcArgs{A: 6, B: 7, Op: "multiply"}, want: "42"},
		{name: "divide", args: CalcArgs{A: 15, B: 4, Op: "divide"}, want: "3.75"},
		{name: "divide_integer", args: CalcArgs{A: 10, B: 2, Op: "divide"}, want: "5"},
		{name: "divide_by_zero", args: CalcArgs{A: 1, B: 0, Op: "divide"}, wantErr: true},
		{name: "unknown_op", args: CalcArgs{A: 1, B: 2, Op: "modulo"}, wantErr: true},
		{name: "negative", args: CalcArgs{A: -3, B: 5, Op: "add"}, want: "2"},
		{name: "float_result", args: CalcArgs{A: 1, B: 3, Op: "divide"}, want: "0.3333333333333333"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argsJSON, _ := json.Marshal(tt.args)
			result, err := calc.Execute(context.Background(), argsJSON)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.want {
				t.Errorf("got %q, want %q", result, tt.want)
			}
		})
	}
}

func TestCalculator_Schema(t *testing.T) {
	calc := NewCalculator()

	if calc.Name() != "calculator" {
		t.Errorf("Name() = %q, want %q", calc.Name(), "calculator")
	}
	if calc.Description() != "Basic arithmetic operations" {
		t.Errorf("Description() = %q", calc.Description())
	}

	schema := calc.Schema()
	if schema.Parameters == nil {
		t.Fatal("Schema().Parameters is nil")
	}
}
