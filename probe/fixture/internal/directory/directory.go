// Package directory is the upstream account service the collector resolves
// records against.
//
// Two properties are deliberate, and anything built on top of this package has
// to account for both:
//
//   - It CANNOT be enumerated. There is no list call, and the address space is
//     the customer's whole mail domain, so the set of keys is unbounded and not
//     known ahead of time.
//   - Records CHANGE here, and nothing downstream is notified. A display name
//     edited upstream is live immediately, so any copy held elsewhere is stale
//     from that moment and there is no event to invalidate it.
//
// Every resolve is a round trip.
package directory

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned when the directory holds no matching account.
var ErrNotFound = errors.New("account not found")

// roundTrip stands in for the upstream network call.
const roundTrip = 5 * time.Millisecond

// Account is one upstream account record.
type Account struct {
	ID    string
	Email string
	Name  string
}

// Client resolves accounts one at a time. Safe for concurrent use.
type Client struct {
	mu       sync.RWMutex
	accounts map[string]Account
}

// New returns a Client against the upstream directory.
func New() *Client {
	return &Client{accounts: map[string]Account{
		"u1": {ID: "u1", Email: "ada@example.com", Name: "Ada Lovelace"},
		"u2": {ID: "u2", Email: "grace@example.com", Name: "Grace Hopper"},
		"u3": {ID: "u3", Email: "alan@example.com", Name: "Alan Turing"},
	}}
}

// ByID resolves one account by id. One round trip per call.
func (c *Client) ByID(id string) (Account, error) {
	time.Sleep(roundTrip)
	c.mu.RLock()
	defer c.mu.RUnlock()
	a, ok := c.accounts[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	return a, nil
}

// ByEmail resolves one account by an already-normalised, upper-cased address.
// One round trip per call.
func (c *Client) ByEmail(email string) (Account, error) {
	time.Sleep(roundTrip)
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, a := range c.accounts {
		if strings.ToUpper(a.Email) == email {
			return a, nil
		}
	}
	return Account{}, ErrNotFound
}
