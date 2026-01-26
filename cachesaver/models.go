package cachesaver

import (
	"unique"
)

type Info struct {
	Name        string
	Street      unique.Handle[string]
	HouseNumber unique.Handle[string]
	City        unique.Handle[string]
	Region      unique.Handle[string]
	Weight      uint8
}
