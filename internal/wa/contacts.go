package wa

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// resolvePNToLID uses the supplied session, falling back to the original JID
// when neither its stored mappings nor a user-info lookup resolves it.
func resolvePNToLID(ctx context.Context, cli *whatsmeow.Client, jid types.JID) types.JID {
	if cli == nil || cli.Store == nil {
		return jid
	}
	pn := jid.ToNonAD()
	if ownPN := cli.Store.GetJID().ToNonAD(); pn == ownPN {
		if ownLID := cli.Store.GetLID().ToNonAD(); !ownLID.IsEmpty() {
			return ownLID
		}
	}
	if cli.Store.LIDs == nil {
		return jid
	}
	lid, err := cli.Store.LIDs.GetLIDForPN(ctx, pn)
	if err == nil && !lid.IsEmpty() {
		return lid
	}
	info, err := cli.GetUserInfo(ctx, []types.JID{pn})
	if err == nil {
		if resolved := info[pn].LID.ToNonAD(); !resolved.IsEmpty() {
			return resolved
		}
	}
	return jid
}

func (c *Client) GetContact(ctx context.Context, jid types.JID) (types.ContactInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || cli.Store == nil || cli.Store.Contacts == nil {
		return types.ContactInfo{}, fmt.Errorf("contacts store not available")
	}
	return cli.Store.Contacts.GetContact(ctx, jid)
}

func (c *Client) ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID {
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || cli.Store == nil || cli.Store.LIDs == nil {
		return jid
	}
	pn, err := cli.Store.LIDs.GetPNForLID(ctx, jid.ToNonAD())
	if err != nil || pn.IsEmpty() {
		return jid
	}
	return pn
}

func (c *Client) ResolvePNToLID(ctx context.Context, jid types.JID) types.JID {
	if jid.Server != types.DefaultUserServer {
		return jid
	}
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	return resolvePNToLID(ctx, cli, jid)
}

// StoreLIDMapping records that a hidden ID (LID) and a phone number are the
// same person, as a history sync's conversation says.
func (c *Client) StoreLIDMapping(ctx context.Context, lid, pn string) {
	l, err1 := types.ParseJID(lid)
	p, err2 := types.ParseJID(pn)
	if err1 != nil || err2 != nil || l.Server != types.HiddenUserServer || p.Server != types.DefaultUserServer {
		return
	}
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || cli.Store == nil || cli.Store.LIDs == nil {
		return
	}
	_ = cli.Store.LIDs.PutLIDMapping(ctx, l.ToNonAD(), p.ToNonAD())
}
