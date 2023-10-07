package glb

import "github.com/lunfardo314/proxima/api"

// InclusionScore returns
func InclusionScore(inclusionData []api.InclusionData, totalSupply uint64) (totalBranches int, percOfTotal int, percOfDominating int) {
	totalBranches = len(inclusionData)
	if totalBranches == 0 {
		return
	}
	var numDominating, numIncluded, numIncludedIntoDominating int
	for i := range inclusionData {
		if inclusionData[i].Coverage > (totalSupply >> 1) {
			numDominating++
		}
		if inclusionData[i].Included {
			numIncluded++
			if inclusionData[i].Coverage > (totalSupply >> 1) {
				numIncludedIntoDominating++
			}
		}
	}
	if numIncluded == 0 {
		return
	}
	percOfTotal = (100 * numIncluded) / totalBranches
	percOfDominating = (100 * numIncludedIntoDominating) / numDominating
	return
}
