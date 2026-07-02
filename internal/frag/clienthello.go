package frag

import "encoding/binary"

// sniSplitOffset parses a TLS ClientHello record in b and returns a byte offset
// that falls strictly inside the SNI host_name value, so splitting the buffer
// there divides the host name across two segments/records and defeats naive DPI
// SNI matching. ok is false when b is not a ClientHello carrying an SNI
// extension, in which case callers should fall back to a fixed offset.
//
// Layout walked (TLS 1.2/1.3 ClientHello):
//
//	record:    type(1)=0x16 | version(2) | length(2)
//	handshake: msg_type(1)=0x01 | length(3)
//	body:      client_version(2) | random(32) |
//	           session_id(1+n) | cipher_suites(2+n) |
//	           compression_methods(1+n) | extensions(2+n)
//	extension: type(2) | length(2) | data
//	SNI data:  list_len(2) | entry_type(1)=0 | name_len(2) | host_name
func sniSplitOffset(b []byte) (int, bool) {
	if len(b) < 5 || b[0] != 0x16 {
		return 0, false
	}
	recLen := int(binary.BigEndian.Uint16(b[3:5]))
	rec := b[5:]
	if recLen < len(rec) {
		rec = rec[:recLen] // trust the record length when present
	}
	if len(rec) < 4 || rec[0] != 0x01 {
		return 0, false
	}

	p := rec[4:] // handshake body
	if len(p) < 34 {
		return 0, false
	}
	p = p[34:] // skip client_version(2) + random(32)

	sidLen, ok := skipVec8(&p)
	if !ok {
		return 0, false
	}
	csLen, ok := skipVec16(&p)
	if !ok {
		return 0, false
	}
	cmLen, ok := skipVec8(&p)
	if !ok {
		return 0, false
	}
	if len(p) < 2 {
		return 0, false
	}
	extTotal := int(binary.BigEndian.Uint16(p[0:2]))
	p = p[2:]
	if extTotal < len(p) {
		p = p[:extTotal]
	}

	// Absolute index in b where the extensions block (current p) begins.
	base := 5 + 4 + 34 + (1 + sidLen) + (2 + csLen) + (1 + cmLen) + 2

	off := 0
	for off+4 <= len(p) {
		extType := binary.BigEndian.Uint16(p[off : off+2])
		extLen := int(binary.BigEndian.Uint16(p[off+2 : off+4]))
		body := off + 4
		if body+extLen > len(p) {
			return 0, false
		}
		if extType == 0x0000 { // server_name
			sn := p[body : body+extLen]
			if len(sn) < 5 { // list_len(2) + entry_type(1) + name_len(2)
				return 0, false
			}
			// The first ServerNameList entry must be a host_name (type 0). If it
			// is not (a GREASE or unknown entry type), name_len at sn[3:5] would
			// not describe a host name, so bail to the fixed fallback offset
			// rather than split at a bogus position.
			if sn[2] != 0 {
				return 0, false
			}
			nameLen := int(binary.BigEndian.Uint16(sn[3:5]))
			nameStart := 5
			if nameStart+nameLen > len(sn) {
				return 0, false
			}
			absNameStart := base + body + nameStart
			if nameLen < 2 {
				return absNameStart, nameLen == 1
			}
			return absNameStart + nameLen/2, true
		}
		off = body + extLen
	}
	return 0, false
}

// skipVec8 advances *p past an 8-bit length-prefixed vector, returning its
// payload length.
func skipVec8(p *[]byte) (int, bool) {
	if len(*p) < 1 {
		return 0, false
	}
	n := int((*p)[0])
	if len(*p) < 1+n {
		return 0, false
	}
	*p = (*p)[1+n:]
	return n, true
}

// skipVec16 advances *p past a 16-bit length-prefixed vector, returning its
// payload length.
func skipVec16(p *[]byte) (int, bool) {
	if len(*p) < 2 {
		return 0, false
	}
	n := int(binary.BigEndian.Uint16((*p)[0:2]))
	if len(*p) < 2+n {
		return 0, false
	}
	*p = (*p)[2+n:]
	return n, true
}
