package secret

import (
	"bytes"
	"testing"
)

func TestCipherRoundTripAndPurposeBinding(t *testing.T) {
	c, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := c.Encrypt("upstream-secret", "site:one")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := c.Decrypt(sealed, "site:one")
	if err != nil || plain != "upstream-secret" {
		t.Fatalf("round trip failed: %q, %v", plain, err)
	}
	if _, err := c.Decrypt(sealed, "site:two"); err == nil {
		t.Fatal("ciphertext must be bound to its purpose")
	}
}
