package cluster

import (
	"context"
	"fmt"
	"sync"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ListableIn answers, for each of namespaces, which of kinds this token may
// list, indexed as kinds is: one rules review per namespace however many kinds
// are asked. A review the apiserver calls incomplete (a webhook authorizer it
// cannot enumerate) is asked kind by kind. A failed review is an error, never
// a smaller answer, which would read as lost access and be cached.
func (c *Client) ListableIn(ctx context.Context, namespaces []string, kinds []schema.GroupResource) (map[string][]bool, error) {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		out      = make(map[string][]bool, len(namespaces))
		firstErr error
		next     = make(chan string)
	)
	for range min(probeConcurrency, len(namespaces)) {
		wg.Go(func() {
			for ns := range next {
				if ctx.Err() != nil {
					continue
				}
				answer, err := c.listable(ctx, ns, kinds)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				out[ns] = answer
				mu.Unlock()
			}
		})
	}
	for _, ns := range namespaces {
		next <- ns
	}
	close(next)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func (c *Client) listable(ctx context.Context, ns string, kinds []schema.GroupResource) ([]bool, error) {
	review := &authzv1.SelfSubjectRulesReview{Spec: authzv1.SelfSubjectRulesReviewSpec{Namespace: ns}}
	res, err := c.kube.AuthorizationV1().SelfSubjectRulesReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("rules review for %s: %w", ns, err)
	}
	answer := make([]bool, len(kinds))
	for i, k := range kinds {
		if !res.Status.Incomplete {
			answer[i] = rulesList(res.Status.ResourceRules, k)
			continue
		}
		if answer[i], err = c.Can(ctx, "list", k.Group, k.Resource, ns); err != nil {
			return nil, err
		}
	}
	return answer, nil
}

// rulesList reports whether rules let their holder list k. A rule naming
// specific objects does not count: a list names none.
func rulesList(rules []authzv1.ResourceRule, k schema.GroupResource) bool {
	for _, r := range rules {
		if len(r.ResourceNames) == 0 && contains(r.APIGroups, k.Group) && contains(r.Resources, k.Resource) && contains(r.Verbs, "list") {
			return true
		}
	}
	return false
}
