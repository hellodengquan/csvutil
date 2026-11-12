package csvutil

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"testing"
)

type nestedCustomValue string

func (v *nestedCustomValue) UnmarshalCSV(data []byte) error {
	*v = nestedCustomValue("decoded:" + string(data))
	return nil
}

func (v nestedCustomValue) MarshalCSV() ([]byte, error) {
	return []byte("encoded:" + string(v)), nil
}

type failingNestedMarshaler struct {
	err error
}

func (v failingNestedMarshaler) MarshalCSV() ([]byte, error) {
	return nil, v.err
}

func TestNestedFieldMappingDeterminism(t *testing.T) {
	t.Run("explicit tag wins over promoted field", func(t *testing.T) {
		type Inner struct {
			Value string
		}
		type Outer struct {
			Inner
			Tagged string `csv:"Value"`
		}

		header, err := Header(Outer{}, "")
		if err != nil {
			t.Fatalf("Header returned error: %v", err)
		}
		if got, want := header, []string{"Value"}; !equalStrings(got, want) {
			t.Fatalf("want header %v; got %v", want, got)
		}

		input := "Value\nfrom-csv\n"
		var decoded Outer
		decoder, err := NewDecoder(csv.NewReader(strings.NewReader(input)))
		if err != nil {
			t.Fatal(err)
		}
		if err := decoder.Decode(&decoded); err != nil {
			t.Fatalf("Decode returned error: %v", err)
		}
		if decoded.Tagged != "from-csv" || decoded.Inner.Value != "" {
			t.Fatalf("want only explicitly tagged field populated; got %+v", decoded)
		}

		var buf bytes.Buffer
		writer := csv.NewWriter(&buf)
		encoder := NewEncoder(writer)
		if err := encoder.Encode(Outer{Inner: Inner{Value: "promoted"}, Tagged: "tagged"}); err != nil {
			t.Fatalf("Encode returned error: %v", err)
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			t.Fatal(err)
		}
		if want := "Value\ntagged\n"; buf.String() != want {
			t.Fatalf("want %q; got %q", want, buf.String())
		}
	})

	t.Run("shallower promoted field wins over deeper field", func(t *testing.T) {
		type Deep struct {
			Value string
		}
		type Middle struct {
			Deep
		}
		type Shallow struct {
			Value string
		}
		type Outer struct {
			Shallow
			Middle
		}

		header, err := Header(Outer{}, "")
		if err != nil {
			t.Fatalf("Header returned error: %v", err)
		}
		if got, want := header, []string{"Value"}; !equalStrings(got, want) {
			t.Fatalf("want header %v; got %v", want, got)
		}

		var value Outer
		decoder, err := NewDecoder(csv.NewReader(strings.NewReader("Value\nchosen\n")))
		if err != nil {
			t.Fatal(err)
		}
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("Decode returned error: %v", err)
		}
		if value.Shallow.Value != "chosen" || value.Middle.Deep.Value != "" {
			t.Fatalf("want shallow field chosen; got %+v", value)
		}
	})

	t.Run("explicit tag wins over shallower promoted field", func(t *testing.T) {
		type Deep struct {
			Value string `csv:"Value"`
		}
		type Middle struct {
			Deep
		}
		type Shallow struct {
			Value string
		}
		type Outer struct {
			Shallow
			Middle
		}

		var value Outer
		decoder, err := NewDecoder(csv.NewReader(strings.NewReader("Value\nchosen\n")))
		if err != nil {
			t.Fatal(err)
		}
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("Decode returned error: %v", err)
		}
		if value.Middle.Deep.Value != "chosen" || value.Shallow.Value != "" {
			t.Fatalf("want explicitly tagged field chosen; got %+v", value)
		}
	})

	t.Run("same-depth competition returns column and paths", func(t *testing.T) {
		type Left struct {
			Value string
		}
		type Right struct {
			Value string
		}
		type Outer struct {
			Left
			Right
		}

		_, err := Header(Outer{}, "")
		var conflict *FieldConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("want FieldConflictError; got %v", err)
		}
		if err.Error() != `csvutil: ambiguous field mapping: column "Value": Left.Value, Right.Value` {
			t.Fatalf("unexpected conflict message: %v", err)
		}
		wantConflict := &FieldConflictError{Conflicts: []FieldConflict{{
			Column: "Value",
			Paths:  []string{"Left.Value", "Right.Value"},
		}}}
		if !equalFieldConflictErrors(conflict, wantConflict) {
			t.Fatalf("want %v; got %v", wantConflict, conflict)
		}

		decoder, err := NewDecoder(csv.NewReader(strings.NewReader("Value\nignored\n")))
		if err != nil {
			t.Fatal(err)
		}
		if err := decoder.Decode(&Outer{}); !equalFieldConflictErrors(err, wantConflict) {
			t.Fatalf("want decode conflict %v; got %v", wantConflict, err)
		}

		var buf bytes.Buffer
		writer := csv.NewWriter(&buf)
		encoder := NewEncoder(writer)
		encoder.AutoHeader = false
		if err := encoder.Encode(Outer{}); !equalFieldConflictErrors(err, wantConflict) {
			t.Fatalf("want encode conflict %v; got %v", wantConflict, err)
		}
		writer.Flush()
		if buf.Len() != 0 {
			t.Fatalf("want no CSV output after conflict; got %q", buf.String())
		}
	})
}

func TestNestedCustomTypeRoundTrip(t *testing.T) {
	type Inner struct {
		Value nestedCustomValue
	}
	type Outer struct {
		Inner
		Name string
	}

	original := []Outer{{
		Inner: Inner{Value: nestedCustomValue("custom")},
		Name:  "name",
	}}
	data, err := Marshal(original)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if want := "Value,Name\nencoded:custom,name\n"; string(data) != want {
		t.Fatalf("want %q; got %q", want, string(data))
	}

	var decoded []Outer
	if err := Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if len(decoded) != 1 || decoded[0].Name != "name" || decoded[0].Inner.Value != "decoded:encoded:custom" {
		t.Fatalf("unexpected decoded value: %+v", decoded)
	}
}

func TestNestedMappingMissingAndEmptyColumns(t *testing.T) {
	type Inner struct {
		Required string
		Optional *int
	}
	type Outer struct {
		Inner
		Other string `csv:"other,omitempty"`
	}

	reader := csv.NewReader(strings.NewReader("Required\n\"\"\n\"\"\n"))
	decoder, err := NewDecoder(reader)
	if err != nil {
		t.Fatal(err)
	}

	var value Outer
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if value.Required != "" || value.Optional != nil || value.Other != "" {
		t.Fatalf("unexpected decoded value: %+v", value)
	}
	strictDecoder, err := NewDecoder(csv.NewReader(strings.NewReader("Required\n\"\"\n")))
	if err != nil {
		t.Fatal(err)
	}
	strictDecoder.DisallowMissingColumns = true
	var incomplete Outer
	err = strictDecoder.Decode(&incomplete)
	var missing *MissingColumnsError
	if !errors.As(err, &missing) {
		t.Fatalf("want MissingColumnsError; got %v", err)
	}
	if want := []string{"Optional", "other"}; !equalStrings(missing.Columns, want) {
		t.Fatalf("want missing columns %v; got %v", want, missing.Columns)
	}
}

func TestDuplicateHeaderRejected(t *testing.T) {
	_, err := NewDecoder(csv.NewReader(strings.NewReader("A,A\nfirst,second\n")))
	decoderWant := &DuplicateHeaderError{Column: "A", Positions: []int{1, 2}}
	if !equalDuplicateHeaderError(err, decoderWant) {
		t.Fatalf("want %v; got %v", decoderWant, err)
	}
	if err.Error() != `csvutil: duplicate header "A" at positions 1, 2` {
		t.Fatalf("unexpected duplicate header message: %v", err)
	}

	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	encoder := NewEncoder(writer)
	encoder.SetHeader([]string{"A", "B", "A"})
	encoderWant := &DuplicateHeaderError{Column: "A", Positions: []int{1, 3}}
	if err := encoder.Encode(struct{ A, B string }{A: "a", B: "b"}); !equalDuplicateHeaderError(err, encoderWant) {
		t.Fatalf("want %v; got %v", encoderWant, err)
	}
	writer.Flush()
	if buf.Len() != 0 {
		t.Fatalf("want no output; got %q", buf.String())
	}
}

func TestDecoderContinuesAfterRecordError(t *testing.T) {
	reader := csv.NewReader(strings.NewReader("Name,Count\nfirst,bad\nsecond,2\n"))
	reader.ReuseRecord = true
	decoder, err := NewDecoder(reader)
	if err != nil {
		t.Fatal(err)
	}

	type Record struct {
		Name  string
		Count int
	}

	var failed Record
	if err := decoder.Decode(&failed); err == nil {
		t.Fatal("want decode error for bad integer")
	}

	var next Record
	if err := decoder.Decode(&next); err != nil {
		t.Fatalf("want next record to decode successfully; got %v", err)
	}
	if next.Name != "second" || next.Count != 2 {
		t.Fatalf("unexpected next record: %+v", next)
	}
	if err := decoder.Decode(new(Record)); err != io.EOF {
		t.Fatalf("want io.EOF; got %v", err)
	}
}

func TestEncoderReusesFieldCacheWithoutRecordState(t *testing.T) {
	type Record struct {
		Name  string
		Value failingNestedMarshaler
	}

	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	encoder := NewEncoder(writer)
	encoder.AutoHeader = false
	marshalErr := errors.New("marshal failed")

	if err := encoder.Encode(Record{Name: "broken", Value: failingNestedMarshaler{err: marshalErr}}); !errors.Is(err, marshalErr) {
		t.Fatalf("want %v; got %v", marshalErr, err)
	}

	if err := encoder.Encode(Record{Name: "good"}); err != nil {
		t.Fatalf("Encode after marshaling error returned error: %v", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatalf("writer returned error after reuse: %v", err)
	}
	if want := "good,\n"; buf.String() != want {
		t.Fatalf("want %q; got %q", want, buf.String())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalFieldConflictErrors(err error, want *FieldConflictError) bool {
	var got *FieldConflictError
	if !errors.As(err, &got) || len(got.Conflicts) != len(want.Conflicts) {
		return false
	}
	for i := range want.Conflicts {
		if got.Conflicts[i].Column != want.Conflicts[i].Column || !equalStrings(got.Conflicts[i].Paths, want.Conflicts[i].Paths) {
			return false
		}
	}
	return true
}

func equalDuplicateHeaderError(err error, want *DuplicateHeaderError) bool {
	var got *DuplicateHeaderError
	return errors.As(err, &got) && got.Column == want.Column && equalInts(got.Positions, want.Positions)
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
