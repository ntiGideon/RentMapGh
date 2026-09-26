package listings

import (
	"errors"
	"fmt"
)

// Status of a listing (mirrors the ent enum).
type Status string

const (
	Draft         Status = "draft"
	PendingReview Status = "pending_review"
	Active        Status = "active"
	Paused        Status = "paused"
	Rented        Status = "rented"
	Expired       Status = "expired"
	Removed       Status = "removed"
)

// Event moves a listing between statuses.
type Event string

const (
	EvSubmitVerified   Event = "submit_verified"   // lister's identity is verified → goes live
	EvSubmitUnverified Event = "submit_unverified" // → moderator review first
	EvApprove          Event = "approve"           // moderator
	EvRequestChanges   Event = "request_changes"   // moderator sends it back to draft
	EvWithdraw         Event = "withdraw"          // lister pulls it out of review
	EvPause            Event = "pause"
	EvResume           Event = "resume" // also used to re-confirm an expired listing
	EvMarkRented       Event = "mark_rented"
	EvRelist           Event = "relist"
	EvExpire           Event = "expire" // system: not confirmed for 30+ days
	EvRemove           Event = "remove" // moderator takedown
)

var ErrTransition = errors.New("listings: transition not allowed")

// transitions is the whole state machine. Anything not listed is refused.
var transitions = map[Status]map[Event]Status{
	Draft: {
		EvSubmitVerified:   Active,
		EvSubmitUnverified: PendingReview,
		EvRemove:           Removed,
	},
	PendingReview: {
		EvApprove:        Active,
		EvRequestChanges: Draft,
		EvWithdraw:       Draft,
		EvRemove:         Removed,
	},
	Active: {
		EvPause:      Paused,
		EvMarkRented: Rented,
		EvExpire:     Expired,
		EvRemove:     Removed,
	},
	Paused: {
		EvResume:     Active,
		EvMarkRented: Rented,
		EvExpire:     Expired,
		EvRemove:     Removed,
	},
	Rented: {
		EvRelist: Active,
		EvRemove: Removed,
	},
	Expired: {
		EvResume: Active,
		EvRemove: Removed,
	},
	Removed: {},
}

// Next returns the status after ev, or ErrTransition.
func Next(from Status, ev Event) (Status, error) {
	to, ok := transitions[from][ev]
	if !ok {
		return from, fmt.Errorf("%w: %s from %s", ErrTransition, ev, from)
	}
	return to, nil
}

// Editable reports whether the lister may still change a listing's content.
// Live listings can be edited too; edits to price/location on an unverified
// lister's listing are re-reviewed in Phase 5.
func Editable(s Status) bool { return s != Removed }

// Public reports whether renters can see the listing.
func Public(s Status) bool { return s == Active }
