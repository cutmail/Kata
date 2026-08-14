package main

import "testing"

// TestStatusIsItsOwnCommand は status が list のエイリアスに戻っていないことを確かめる。
// エイリアスに戻ると status 固有の終了コードが失われ、CI での利用が黙って壊れる。
func TestStatusIsItsOwnCommand(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"status"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name() != "status" {
		t.Fatalf("status resolved to %q, want status", cmd.Name())
	}
}

// TestListKeepsShortAlias は ls が list を指し続けることを確かめる。
func TestListKeepsShortAlias(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"ls"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name() != "list" {
		t.Fatalf("ls resolved to %q, want list", cmd.Name())
	}
}
