package server

// func escape(s string) string {
// 	return html.EscapeString(s)
// }

// func writeGeoInfoFast(buf *bytes.Buffer, i geomodel.Info) {
// 	// return []byte(fmt.Sprintf(`{"region":"%s","city":"%s","street":"%s","house_number":"%s"}`, p.Region, p.City, p.Street, p.HouseNumber))
// 	buf.WriteString(`{`)
// 	buf.WriteString(`"name":"`)
// 	buf.WriteString(escape(i.Name))
// 	buf.WriteString(`","street":"`)
// 	buf.WriteString(escape(i.Street))
// 	buf.WriteString(`"region":"`)
// 	buf.WriteString(escape(i.Region))
// 	buf.WriteString(`","city":"`)
// 	buf.WriteString(escape(i.City))

// 	buf.WriteString(`","house_number":"`)
// 	buf.WriteString(escape(i.HouseNumber))
// 	buf.WriteString(`"}`)
// }

// func writeGeoInfoListFast(buf *bytes.Buffer, infos []geomodel.Info) {
// 	buf.WriteRune('[')
// 	for i, v := range infos {
// 		writeGeoInfoFast(buf, v)
// 		if i != len(infos)-1 {
// 			buf.WriteRune(',')
// 		}

// 	}
// 	buf.WriteByte(']')
// }
