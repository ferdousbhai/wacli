package main

import "testing"

func TestExportChatName(t *testing.T) {
	for in, want := range map[string]string{
		"/x/WhatsApp Chat - Maya.zip":   "Maya",
		"/x/WhatsApp Chat - Dr. Smith":  "Dr. Smith",
		"/x/WhatsApp Chat - Acme Inc./": "Acme Inc.",
		"WhatsApp Chat - Dr. Smith.ZIP": "Dr. Smith",
	} {
		if got := exportChatName(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
