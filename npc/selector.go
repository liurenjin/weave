package npc

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"github.com/weaveworks/weave/common"
	"github.com/weaveworks/weave/npc/ipset"
)

type selectorSpec struct {
	key      string          // string representation (for hash keying/equality comparison)
	selector labels.Selector // k8s Selector object (for matching)
	dst      bool            // destination selector

	ipsetType ipset.Type // type of ipset to provision
	ipsetName ipset.Name // generated ipset name
	nsName    string     // Namespace name
}

func newSelectorSpec(json *metav1.LabelSelector, dst bool, nsName string, ipsetType ipset.Type) (*selectorSpec, error) {
	selector, err := metav1.LabelSelectorAsSelector(json)
	if err != nil {
		return nil, err
	}
	key := selector.String()
	return &selectorSpec{
		key:      key,
		selector: selector,
		dst:      dst,
		// We prefix the selector string with the namespace name when generating
		// the shortname because you can specify the same selector in multiple
		// namespaces - we need those to map to distinct ipsets
		ipsetName: ipset.Name(IpsetNamePrefix + shortName(nsName+":"+key)),
		ipsetType: ipsetType,
		nsName:    nsName}, nil
}

type selector struct {
	ips  ipset.Interface
	spec *selectorSpec
}

func (s *selector) matches(labelMap map[string]string) bool {
	return s.spec.selector.Matches(labels.Set(labelMap))
}

func (s *selector) addEntry(entry string, comment string) error {
	return s.ips.AddEntry(s.spec.ipsetName, entry, comment)
}

func (s *selector) delEntry(entry string) error {
	return s.ips.DelEntry(s.spec.ipsetName, entry)
}

type selectorFn func(selector *selector) error

type selectorSet struct {
	ips           ipset.Interface
	onNewSelector selectorFn
	users         map[string]map[types.UID]struct{} // list of users per selector
	entries       map[string]*selector
}

func newSelectorSet(ips ipset.Interface, onNewSelector selectorFn) *selectorSet {
	return &selectorSet{
		ips:           ips,
		onNewSelector: onNewSelector,
		users:         make(map[string]map[types.UID]struct{}),
		entries:       make(map[string]*selector)}
}

// TODO(mp) document `found`
func (ss *selectorSet) addToMatching(labelMap map[string]string, entry string, comment string) (bool, error) {
	found := false
	for _, s := range ss.entries {
		if s.matches(labelMap) {
			if s.spec.dst {
				found = true
			}
			if err := s.addEntry(entry, comment); err != nil {
				return found, err
			}
		}
	}
	return found, nil
}

func (ss *selectorSet) delFromMatching(labelMap map[string]string, entry string) error {
	for _, s := range ss.entries {
		if s.matches(labelMap) {
			if err := s.delEntry(entry); err != nil {
				return err
			}
		}
	}
	return nil
}

func (ss *selectorSet) deprovision(user types.UID, current, desired map[string]*selectorSpec) error {
	for key, spec := range current {
		if _, found := desired[key]; !found {
			delete(ss.users[key], user)
			if len(ss.users[key]) == 0 {
				common.Log.Infof("destroying ipset: %#v", spec)
				if err := ss.ips.Destroy(spec.ipsetName); err != nil {
					return err
				}
				delete(ss.entries, key)
				delete(ss.users, key)
			}
		}
	}
	return nil
}

func (ss *selectorSet) provision(user types.UID, current, desired map[string]*selectorSpec) error {
	for key, spec := range desired {
		if _, found := current[key]; !found {
			if _, found := ss.users[key]; !found {
				common.Log.Infof("creating ipset: %#v", spec)
				if err := ss.ips.Create(spec.ipsetName, spec.ipsetType); err != nil {
					return err
				}
				selector := &selector{ss.ips, spec}
				if err := ss.onNewSelector(selector); err != nil {
					return err
				}
				ss.users[key] = make(map[types.UID]struct{})
				ss.entries[key] = selector
			}
			ss.users[key][user] = struct{}{}
		}
	}
	return nil
}
