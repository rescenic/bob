package dialect

import (
	"io"

	"github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/clause"
)

// Trying to represent the select query structure as documented in
// https://www.sqlite.org/lang_update.html
type UpdateQuery struct {
	clause.With
	or
	Table clause.From
	clause.Set
	clause.From
	clause.Where
	clause.Returning
}

func (u UpdateQuery) WriteSQL(w io.Writer, d bob.Dialect, start int) ([]any, error) {
	var args []any

	withArgs, err := bob.ExpressIf(w, d, start+len(args), u.With,
		len(u.With.CTEs) > 0, "\n", "")
	if err != nil {
		return nil, err
	}
	args = append(args, withArgs...)

	w.Write([]byte("UPDATE"))

	_, err = bob.ExpressIf(w, d, start+len(args), u.or, true, " ", "")
	if err != nil {
		return nil, err
	}

	tableArgs, err := bob.ExpressIf(w, d, start+len(args), u.Table, true, " ", "")
	if err != nil {
		return nil, err
	}
	args = append(args, tableArgs...)

	setArgs, err := bob.ExpressIf(w, d, start+len(args), u.Set, true, " ", "")
	if err != nil {
		return nil, err
	}
	args = append(args, setArgs...)

	fromArgs, err := bob.ExpressIf(w, d, start+len(args), u.From,
		u.From.Table != nil, "\nFROM ", "")
	if err != nil {
		return nil, err
	}
	args = append(args, fromArgs...)

	whereArgs, err := bob.ExpressIf(w, d, start+len(args), u.Where,
		len(u.Where.Conditions) > 0, "\n", "")
	if err != nil {
		return nil, err
	}
	args = append(args, whereArgs...)

	retArgs, err := bob.ExpressIf(w, d, start+len(args), u.Returning,
		len(u.Returning.Expressions) > 0, "\n", "")
	if err != nil {
		return nil, err
	}
	args = append(args, retArgs...)

	return args, nil
}
