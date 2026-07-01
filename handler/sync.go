package handler

import "sort"

type NodeSynchronizer interface {
	SyncNodes(nodeIDs []int) []NodeSyncResult
}

func unionIDs(groups ...[]int) []int {
	seen := make(map[int]struct{})
	for _, ids := range groups {
		for _, id := range ids {
			if id > 0 {
				seen[id] = struct{}{}
			}
		}
	}
	result := make([]int, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Ints(result)
	return result
}

func changedIDs(before, after []int) []int {
	beforeSet := make(map[int]bool, len(before))
	afterSet := make(map[int]bool, len(after))
	for _, id := range before {
		beforeSet[id] = true
	}
	for _, id := range after {
		afterSet[id] = true
	}
	var changed []int
	for id := range beforeSet {
		if !afterSet[id] {
			changed = append(changed, id)
		}
	}
	for id := range afterSet {
		if !beforeSet[id] {
			changed = append(changed, id)
		}
	}
	sort.Ints(changed)
	return changed
}

func syncNodes(syncer NodeSynchronizer, nodeIDs []int) []NodeSyncResult {
	if syncer == nil || len(nodeIDs) == 0 {
		return []NodeSyncResult{}
	}
	return syncer.SyncNodes(nodeIDs)
}
