package main

import (
	"fmt"
	"testing"
)

func TestGetRecentConversations(t *testing.T) {
	convs, err := getRecentConversations(10)
	fmt.Printf("DEBUG: convs=%+v, err=%v\n", convs, err)
	if err != nil {
		t.Fatalf("Error: %v", err)
	}
	if len(convs) == 0 {
		t.Fatalf("No conversations returned")
	}
}
