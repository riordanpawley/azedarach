package daemonclient

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestAIAccountClientCommands(t *testing.T) {
	tests := []struct {
		name    string
		command string
		call    func(*Client) error
	}{
		{
			name:    "backup",
			command: protocol.CommandAIAccountBackup,
			call: func(client *Client) error {
				_, err := client.BackupAIAccount(context.Background(), protocol.AIAccountBackupRequestBody{Provider: protocol.AIAccountProviderClaude, Name: "work"})
				return err
			},
		},
		{
			name:    "list",
			command: protocol.CommandAIAccountList,
			call: func(client *Client) error {
				_, err := client.ListAIAccounts(context.Background(), protocol.AIAccountListRequestBody{})
				return err
			},
		},
		{
			name:    "status",
			command: protocol.CommandAIAccountStatus,
			call: func(client *Client) error {
				_, err := client.StatusAIAccounts(context.Background(), protocol.AIAccountStatusRequestBody{})
				return err
			},
		},
		{
			name:    "activate",
			command: protocol.CommandAIAccountActivate,
			call: func(client *Client) error {
				_, err := client.ActivateAIAccount(context.Background(), protocol.AIAccountActivateRequestBody{Provider: protocol.AIAccountProviderCodex, Name: "personal"})
				return err
			},
		},
		{
			name:    "delete",
			command: protocol.CommandAIAccountDelete,
			call: func(client *Client) error {
				_, err := client.DeleteAIAccount(context.Background(), protocol.AIAccountDeleteRequestBody{Provider: protocol.AIAccountProviderCodex, Name: "personal", Confirm: true})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &specRecordingTransport{replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != tt.command {
					t.Fatalf("command = %q, want %q", req.Command, tt.command)
				}
				body, err := json.Marshal(map[string]any{})
				if err != nil {
					t.Fatal(err)
				}
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: body}, nil
			}}
			if err := tt.call(New(transport)); err != nil {
				t.Fatalf("client command: %v", err)
			}
		})
	}
}
