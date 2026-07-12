package daemonclient

import (
	"context"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func (c *Client) ListBoardViews(ctx context.Context) (protocol.BoardViewListResponseBody, error) {
	body := protocol.BoardViewListRequestBody{}
	if strings.TrimSpace(c.projectID.String()) != "" {
		body.ProjectID = c.projectID
	}
	var out protocol.BoardViewListResponseBody
	if err := c.commandJSON(ctx, protocol.CommandBoardViewList, body, &out); err != nil {
		return protocol.BoardViewListResponseBody{}, err
	}
	return out, nil
}

// ListGlobalViews returns user-level view definitions and per-consumer selections.
func (c *Client) ListGlobalViews(ctx context.Context) (protocol.BoardViewListResponseBody, error) {
	body := protocol.BoardViewListRequestBody{ProjectID: "global"}
	var out protocol.BoardViewListResponseBody
	if err := c.commandJSON(ctx, protocol.CommandBoardViewList, body, &out); err != nil {
		return protocol.BoardViewListResponseBody{}, err
	}
	return out, nil
}

func (c *Client) GetBoardView(ctx context.Context, viewID string) (protocol.BoardViewResponseBody, error) {
	body := protocol.BoardViewGetRequestBody{ViewID: strings.TrimSpace(viewID)}
	if strings.TrimSpace(c.projectID.String()) != "" {
		body.ProjectID = c.projectID
	}
	var out protocol.BoardViewResponseBody
	if err := c.commandJSON(ctx, protocol.CommandBoardViewGet, body, &out); err != nil {
		return protocol.BoardViewResponseBody{}, err
	}
	return out, nil
}

func (c *Client) SaveBoardView(ctx context.Context, view domain.BoardView) (protocol.BoardViewResponseBody, error) {
	body := protocol.BoardViewSaveRequestBody{View: view}
	if strings.TrimSpace(c.projectID.String()) != "" {
		body.ProjectID = c.projectID
	}
	var out protocol.BoardViewResponseBody
	if err := c.commandJSON(ctx, protocol.CommandBoardViewSave, body, &out); err != nil {
		return protocol.BoardViewResponseBody{}, err
	}
	return out, nil
}

func (c *Client) SaveGlobalView(ctx context.Context, record protocol.GlobalViewRecord) (protocol.BoardViewResponseBody, error) {
	body := protocol.BoardViewSaveRequestBody{ProjectID: "global", View: record.View, Scope: record.Scope}
	var out protocol.BoardViewResponseBody
	if err := c.commandJSON(ctx, protocol.CommandBoardViewSave, body, &out); err != nil {
		return protocol.BoardViewResponseBody{}, err
	}
	return out, nil
}

func (c *Client) DeleteBoardView(ctx context.Context, viewID string) error {
	body := protocol.BoardViewDeleteRequestBody{ViewID: strings.TrimSpace(viewID)}
	if strings.TrimSpace(c.projectID.String()) != "" {
		body.ProjectID = c.projectID
	}
	return c.commandJSON(ctx, protocol.CommandBoardViewDelete, body, nil)
}

func (c *Client) DeleteGlobalView(ctx context.Context, viewID string) error {
	body := protocol.BoardViewDeleteRequestBody{ProjectID: "global", ViewID: strings.TrimSpace(viewID)}
	return c.commandJSON(ctx, protocol.CommandBoardViewDelete, body, nil)
}

func (c *Client) SelectBoardView(ctx context.Context, viewID string) (protocol.BoardViewSelectResponseBody, error) {
	body := protocol.BoardViewSelectRequestBody{ViewID: strings.TrimSpace(viewID)}
	if strings.TrimSpace(c.projectID.String()) != "" {
		body.ProjectID = c.projectID
	}
	var out protocol.BoardViewSelectResponseBody
	if err := c.commandJSON(ctx, protocol.CommandBoardViewSelect, body, &out); err != nil {
		return protocol.BoardViewSelectResponseBody{}, err
	}
	return out, nil
}

func (c *Client) SelectBoardViewForProject(ctx context.Context, projectID, viewID string) (protocol.BoardViewSelectResponseBody, error) {
	body := protocol.BoardViewSelectRequestBody{
		ProjectID: naming.ProjectID(strings.TrimSpace(projectID)),
		ViewID:    strings.TrimSpace(viewID),
	}
	var out protocol.BoardViewSelectResponseBody
	if err := c.commandJSON(ctx, protocol.CommandBoardViewSelect, body, &out); err != nil {
		return protocol.BoardViewSelectResponseBody{}, err
	}
	return out, nil
}

func (c *Client) SelectGlobalView(ctx context.Context, consumer protocol.GlobalViewConsumer, viewID string) (protocol.BoardViewSelectResponseBody, error) {
	body := protocol.BoardViewSelectRequestBody{ProjectID: "global", ViewID: strings.TrimSpace(viewID), Consumer: consumer}
	var out protocol.BoardViewSelectResponseBody
	if err := c.commandJSON(ctx, protocol.CommandBoardViewSelect, body, &out); err != nil {
		return out, err
	}
	return out, nil
}
