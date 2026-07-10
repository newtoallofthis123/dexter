package lspclient

import "testing"

func TestDocumentPositionUsesUTF16CodeUnits(t *testing.T) {
	doc := &Document{Path: "fixture.ex", Text: "a🔥target target"}

	first, err := doc.Position("target", 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Line != 0 || first.Character != 3 {
		t.Fatalf("first position = %+v, want line 0 character 3", first)
	}

	second, err := doc.Position("target", 1)
	if err != nil {
		t.Fatal(err)
	}
	if second.Line != 0 || second.Character != 10 {
		t.Fatalf("second position = %+v, want line 0 character 10", second)
	}
}
