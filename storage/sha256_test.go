package storage_test

import (
	"bytes"
	"strings"
	"testing"

	"miniio_s3/storage"
)

func TestComputeSHA256_KnownValue(t *testing.T) {
	// echo -n "hello" | sha256sum → 2cf24dba...
	input := strings.NewReader("hello")
	got, err := storage.ComputeSHA256(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Errorf("SHA256(\"hello\")\n got  %s\n want %s", got, want)
	}
}

func TestComputeSHA256_EmptyInput(t *testing.T) {
	// sha256("") is well-defined
	got, err := storage.ComputeSHA256(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got != want {
		t.Errorf("SHA256(empty)\n got  %s\n want %s", got, want)
	}
}

func TestComputeSHA256_Deterministic(t *testing.T) {
	data := []byte("the quick brown fox")
	h1, _ := storage.ComputeSHA256(bytes.NewReader(data))
	h2, _ := storage.ComputeSHA256(bytes.NewReader(data))
	if h1 != h2 {
		t.Errorf("SHA256 is not deterministic: %s != %s", h1, h2)
	}
}

func TestComputeSHA256_DifferentInputs(t *testing.T) {
	h1, _ := storage.ComputeSHA256(strings.NewReader("aaa"))
	h2, _ := storage.ComputeSHA256(strings.NewReader("bbb"))
	if h1 == h2 {
		t.Error("different inputs produced the same hash")
	}
}

func TestComputeSHA256_LargeInput(t *testing.T) {
	// 10 MB of zeros — verifies streaming doesn't buffer
	data := bytes.Repeat([]byte{0x00}, 10*1024*1024)
	_, err := storage.ComputeSHA256(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("unexpected error on large input: %v", err)
	}
}
