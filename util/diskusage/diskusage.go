package diskusage

var _getDiskUsage func(path string) (total uint64, available uint64, free uint64)

func GetDiskUsage(path string) (total uint64, available uint64, free uint64) {
	if _getDiskUsage == nil {
		return
	}
	return _getDiskUsage(path)
}
