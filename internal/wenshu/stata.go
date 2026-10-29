package wenshu

// Minimal streaming writer for the documented Stata 118 (UTF-8) format.
// Numeric columns use double, short strings use str#, long text uses strL.
import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
)

type Column struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Numeric bool   `json:"numeric"`
}

func eachRow(path string, fn func([]any, uint64) error) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	dec.UseNumber()
	var n uint64
	for {
		var row []any
		e := dec.Decode(&row)
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		n++
		if e = fn(row, n); e != nil {
			return e
		}
	}
}
func WriteDTA(path, spool string, cols []Column) error {
	if len(cols) == 0 || len(cols) > 32767 {
		return fmt.Errorf("Stata 118 变量数必须为 1..32767")
	}
	widths := make([]int, len(cols))
	var n uint64
	e := eachRow(spool, func(row []any, i uint64) error {
		if len(row) != len(cols) {
			return fmt.Errorf("导出列数不一致")
		}
		n = i
		for k, c := range cols {
			if !c.Numeric {
				widths[k] = max(widths[k], len(str(row[k])))
			} else if row[k] != nil {
				v, e := asNumber(row[k])
				if e != nil || math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 8.988465674311579e307 {
					return fmt.Errorf("Stata 数值列 %s 值非法", c.Name)
				}
			}
		}
		return nil
	})
	if e != nil {
		return e
	}
	f, e := os.Create(path)
	if e != nil {
		return e
	}
	defer f.Close()
	var werr error
	write := func(b []byte) {
		if werr == nil {
			_, werr = f.Write(b)
		}
	}
	s := func(x string) { write([]byte(x)) }
	u16 := func(v uint16) { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); write(b) }
	u64 := func(v uint64) { b := make([]byte, 8); binary.LittleEndian.PutUint64(b, v); write(b) }
	pad := func(x string, n int) { b := make([]byte, n); copy(b, x); write(b) }
	pos := func() uint64 {
		x, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			werr = err
		}
		return uint64(x)
	}
	s("<stata_dta><header><release>118</release><byteorder>LSF</byteorder><K>")
	u16(uint16(len(cols)))
	s("</K><N>")
	u64(n)
	s("</N><label>")
	label := "裁判文书本地语料"
	u16(uint16(len(label)))
	s(label)
	s("</label><timestamp>")
	ts := time.Now().In(Shanghai).Format("02 Jan 2006 15:04")
	write([]byte{byte(len(ts))})
	s(ts)
	s("</timestamp></header>")
	offsets := make([]uint64, 14)
	offsets[1] = pos()
	s("<map>")
	for range 14 {
		u64(0)
	}
	s("</map>")
	offsets[2] = pos()
	s("<variable_types>")
	for k, c := range cols {
		if c.Numeric {
			u16(65526)
		} else if widths[k] > 2045 {
			u16(32768)
		} else {
			u16(uint16(max(1, widths[k])))
		}
	}
	s("</variable_types>")
	offsets[3] = pos()
	s("<varnames>")
	for _, c := range cols {
		if !varName.MatchString(c.Name) {
			return fmt.Errorf("无效 Stata 变量名 %s", c.Name)
		}
		pad(c.Name, 129)
	}
	s("</varnames>")
	offsets[4] = pos()
	s("<sortlist>")
	write(make([]byte, 2*(len(cols)+1)))
	s("</sortlist>")
	offsets[5] = pos()
	s("<formats>")
	for _, c := range cols {
		if c.Numeric {
			pad("%10.0g", 57)
		} else {
			pad("%20s", 57)
		}
	}
	s("</formats>")
	offsets[6] = pos()
	s("<value_label_names>")
	write(make([]byte, 129*len(cols)))
	s("</value_label_names>")
	offsets[7] = pos()
	s("<variable_labels>")
	for _, c := range cols {
		r := []rune(c.Label)
		if len(r) > 80 {
			r = r[:80]
		}
		pad(string(r), 321)
	}
	s("</variable_labels>")
	offsets[8] = pos()
	s("<characteristics></characteristics>")
	offsets[9] = pos()
	s("<data>")
	e = eachRow(spool, func(row []any, i uint64) error {
		for k, c := range cols {
			if c.Numeric {
				if row[k] == nil {
					u64(0x7fe0000000000000)
				} else {
					v, e := asNumber(row[k])
					if e != nil {
						return e
					}
					u64(math.Float64bits(v))
				}
			} else {
				v := str(row[k])
				if strings.ContainsRune(v, 0) {
					return fmt.Errorf("字符串 %s 包含 NUL，无法无损写入 Stata", c.Name)
				}
				if widths[k] > 2045 {
					if v == "" {
						u64(0)
					} else {
						u64(uint64(k+1) | (i << 16))
					}
				} else {
					pad(v, max(1, widths[k]))
				}
			}
		}
		return werr
	})
	if e != nil {
		return e
	}
	s("</data>")
	offsets[10] = pos()
	s("<strls>")
	e = eachRow(spool, func(row []any, i uint64) error {
		for k, c := range cols {
			if c.Numeric || widths[k] <= 2045 {
				continue
			}
			v := str(row[k])
			if v == "" {
				continue
			}
			if uint64(len(v)) >= uint64(math.MaxUint32) {
				return fmt.Errorf("strL 长度超限")
			}
			s("GSO")
			b := make([]byte, 4)
			binary.LittleEndian.PutUint32(b, uint32(k+1))
			write(b)
			u64(i)
			write([]byte{130})
			binary.LittleEndian.PutUint32(b, uint32(len(v)+1))
			write(b)
			s(v)
			write([]byte{0})
		}
		return werr
	})
	if e != nil {
		return e
	}
	s("</strls>")
	offsets[11] = pos()
	s("<value_labels></value_labels>")
	offsets[12] = pos()
	s("</stata_dta>")
	offsets[13] = pos()
	if _, e = f.Seek(int64(offsets[1])+int64(len("<map>")), io.SeekStart); e != nil {
		return e
	}
	for _, v := range offsets {
		u64(v)
	}
	if werr != nil {
		return werr
	}
	return f.Sync()
}
