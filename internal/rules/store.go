package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

var ErrNotFound = errors.New("rule not found")

const (
	rulesHash = "ratelimit:rules"
	Wildcard  = "*"
)

// Store persists rules in a single Redis hash, so every instance sees updates
// immediately with no restart and no cache invalidation to get wrong.
//
// Rules are read from Redis on every evaluation. That is a second round trip
// per decision. The alternative — an in-process cache with a short TTL — saves
// it but introduces a window where instances enforce different limits for the
// same tenant. At this scale the round trip is the better trade; see README.
type Store struct {
	rdb *redis.Client
}

func NewStore(rdb *redis.Client) *Store {
	return &Store{rdb: rdb}
}

// field is the hash field for a rule. Identifier and namespace are joined with
// a byte that cannot appear in either, so "a|b" + "c" and "a" + "b|c" cannot
// collide.
func field(identifier, namespace string) string {
	return identifier + "\x00" + namespace
}

func (s *Store) Put(ctx context.Context, r Rule) error {
	if err := r.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal rule: %w", err)
	}
	return s.rdb.HSet(ctx, rulesHash, field(r.Identifier, r.Namespace), b).Err()
}

func (s *Store) Delete(ctx context.Context, identifier, namespace string) error {
	n, err := s.rdb.HDel(ctx, rulesHash, field(identifier, namespace)).Result()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) List(ctx context.Context) ([]Rule, error) {
	all, err := s.rdb.HGetAll(ctx, rulesHash).Result()
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(all))
	for _, raw := range all {
		var r Rule
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			continue // skip corrupt entries rather than failing the whole list
		}
		out = append(out, r)
	}
	return out, nil
}

// Resolve finds the most specific rule for an identifier and namespace.
//
// Precedence, most specific first:
//
//  1. exact identifier + exact namespace   — this tenant, this resource
//  2. wildcard identifier + exact namespace — everyone, this resource
//  3. exact identifier + wildcard namespace — this tenant, everything
//  4. wildcard + wildcard                   — the global default
//
// Rule 2 beats rule 3 deliberately: a per-resource limit is a property of the
// resource being protected (an expensive endpoint stays expensive for
// everyone), whereas a per-tenant blanket limit is a coarser statement. The
// opposite order is defensible; what matters is that the order is fixed,
// documented, and not dependent on map iteration.
//
// Returns ErrNotFound when nothing matches. The caller decides whether an
// unmatched request is allowed or denied — see FailOpen in the HTTP layer.
func (s *Store) Resolve(ctx context.Context, identifier, namespace string) (Rule, error) {
	candidates := []string{
		field(identifier, namespace),
		field(Wildcard, namespace),
		field(identifier, Wildcard),
		field(Wildcard, Wildcard),
	}

	// One round trip for all four candidates rather than four sequential gets.
	vals, err := s.rdb.HMGet(ctx, rulesHash, candidates...).Result()
	if err != nil {
		return Rule{}, err
	}

	for _, v := range vals {
		raw, ok := v.(string)
		if !ok {
			continue // nil: this candidate does not exist
		}
		var r Rule
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			continue
		}
		return r, nil
	}

	return Rule{}, ErrNotFound
}
