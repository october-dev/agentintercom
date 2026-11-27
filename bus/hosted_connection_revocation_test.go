package bus

import "testing"

func TestHostedAdminRevocationCannotBeUndoneByReplacement(t *testing.T) {
	f := newHostedOAuthFixture(t)
	a, ctx := f.h.accounts, f.ctx
	member := hostedTestUser(t, "revoked-member")
	invite, err := a.Invite(ctx, f.user, f.workspace.ID, member.Email, "member")
	requireNoError(t, err)
	_, err = a.AcceptInvite(ctx, member, invite.Token)
	requireNoError(t, err)

	// An owner or admin revokes the member's key; the member cannot restore it.
	revoked, err := a.CreateMCPKey(ctx, member, f.workspace.ID, "Member app", "")
	requireNoError(t, err)
	requireNoError(t, a.RevokeDevice(ctx, f.user, f.workspace.ID, revoked.ID))
	_, err = a.CreateMCPKey(ctx, member, f.workspace.ID, "Member app", revoked.ID)
	requireCode(t, err, CodePermissionDenied)
	machine, err := a.EnrollDevice(ctx, member, f.workspace.ID, "Member machine")
	requireNoError(t, err)
	requireNoError(t, a.RevokeDevice(ctx, f.user, f.workspace.ID, machine.ID))
	pairing, err := a.BeginDevicePairing(ctx, "Member machine")
	requireNoError(t, err)
	err = a.completeDevicePairing(ctx, member, pairing.UserCode, f.workspace.ID, machine.ID, true)
	requireCode(t, err, CodePermissionDenied)
	devices, err := a.Devices(ctx, f.user, f.workspace.ID)
	requireNoError(t, err)
	for _, d := range devices {
		if d.ID == revoked.ID || d.ID == machine.ID {
			require(t, d.RevokedAt != "", "revoked connection %s was restored", d.ID)
		}
	}

	// A connection its owner revoked can still be replaced, keeping its inbox.
	own, err := a.CreateMCPKey(ctx, member, f.workspace.ID, "Own app", "")
	requireNoError(t, err)
	requireNoError(t, a.RevokeDevice(ctx, member, f.workspace.ID, own.ID))
	replaced, err := a.CreateMCPKey(ctx, member, f.workspace.ID, "Own app", own.ID)
	requireNoError(t, err)
	require(t, replaced.ID == own.ID && replaced.Token != own.Token, "self-revoked connection was not replaced in place")

	// An admin revoking an already self-revoked connection still blocks restoring it.
	requireNoError(t, a.RevokeDevice(ctx, member, f.workspace.ID, own.ID))
	requireNoError(t, a.RevokeDevice(ctx, f.user, f.workspace.ID, own.ID))
	_, err = a.CreateMCPKey(ctx, member, f.workspace.ID, "Own app", own.ID)
	requireCode(t, err, CodePermissionDenied)
}
