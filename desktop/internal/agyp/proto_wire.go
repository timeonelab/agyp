package agyp

type protoVal struct {
	varint uint64
	raw    []byte
	wire   uint8
}

func readProtoVarint(data []byte, offset int) (uint64, int, bool) {
	var val uint64
	var shift uint
	for shift < 70 && offset < len(data) {
		b := data[offset]
		offset++
		val |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return val, offset, true
		}
		shift += 7
	}
	return 0, offset, false
}

// parseProtoFields decodes top-level protobuf wire fields without requiring a compiled schema.
// On truncated or unexpected wire types it returns all fields decoded up to that point.
func parseProtoFields(data []byte) map[uint32][]protoVal {
	res := make(map[uint32][]protoVal)
	offset := 0
	for offset < len(data) {
		key, next, ok := readProtoVarint(data, offset)
		if !ok {
			break
		}
		offset = next
		fieldNum := uint32(key >> 3)
		wire := uint8(key & 7)
		if fieldNum == 0 {
			break
		}
		var pv protoVal
		pv.wire = wire
		switch wire {
		case 0:
			v, n2, ok2 := readProtoVarint(data, offset)
			if !ok2 {
				return res
			}
			pv.varint = v
			offset = n2
		case 1:
			if offset+8 > len(data) {
				return res
			}
			pv.raw = data[offset : offset+8]
			offset += 8
		case 2:
			ln, n2, ok2 := readProtoVarint(data, offset)
			if !ok2 {
				return res
			}
			offset = n2
			if ln > uint64(len(data)-offset) {
				return res
			}
			pv.raw = data[offset : offset+int(ln)]
			offset += int(ln)
		case 5:
			if offset+4 > len(data) {
				return res
			}
			pv.raw = data[offset : offset+4]
			offset += 4
		default:
			return res
		}
		res[fieldNum] = append(res[fieldNum], pv)
	}
	return res
}

func protoFirstVarint(m map[uint32][]protoVal, field uint32) (uint64, bool) {
	vals := m[field]
	if len(vals) == 0 || vals[0].wire != 0 {
		return 0, false
	}
	return vals[0].varint, true
}

func protoVarintOrZero(m map[uint32][]protoVal, field uint32) int64 {
	v, ok := protoFirstVarint(m, field)
	if !ok {
		return 0
	}
	return int64(v)
}

func protoFirstBytes(m map[uint32][]protoVal, field uint32) []byte {
	vals := m[field]
	if len(vals) == 0 || vals[0].wire != 2 {
		return nil
	}
	return vals[0].raw
}

func protoFirstString(m map[uint32][]protoVal, field uint32) string {
	b := protoFirstBytes(m, field)
	if len(b) == 0 {
		return ""
	}
	return string(b)
}

func protoNested(m map[uint32][]protoVal, field uint32) map[uint32][]protoVal {
	b := protoFirstBytes(m, field)
	if len(b) == 0 {
		return nil
	}
	return parseProtoFields(b)
}

func protoTimestampMs(b []byte) (float64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	m := parseProtoFields(b)
	sec, hasSec := protoFirstVarint(m, 1)
	nanos, hasNanos := protoFirstVarint(m, 2)
	if !hasSec && !hasNanos {
		return 0, false
	}
	return float64(int64(sec))*1000.0 + float64(int64(nanos))/1e6, true
}

func protoStepIndices(vals []protoVal) []int {
	var out []int
	for _, pv := range vals {
		if pv.wire == 0 {
			out = append(out, int(pv.varint))
		} else if pv.wire == 2 {
			off := 0
			for off < len(pv.raw) {
				v, next, ok := readProtoVarint(pv.raw, off)
				if !ok {
					break
				}
				out = append(out, int(v))
				off = next
			}
		}
	}
	return out
}
