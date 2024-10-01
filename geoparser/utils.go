package geoparser

func firstNotEmpty(in ...string) string {
	for _, v := range in {
		if v != "" {
			return v
		}
	}
	return ""
}
