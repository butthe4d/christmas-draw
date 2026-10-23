package round

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/butthe4d/christmas-draw/internal/store"
)

var (
	ErrInvalidItem = errors.New("invalid item")
	ErrNotEditable = errors.New("wish list is frozen")
)

// ItemError is an ErrInvalidItem carrying the locale key of the reason.
type ItemError struct{ Key string }

func (e *ItemError) Error() string        { return "invalid item: " + e.Key }
func (e *ItemError) Is(target error) bool { return target == ErrInvalidItem }

type ItemInput struct{ Title, Link, Note string }

type ReceiverView struct {
	Name  string
	Items []store.Item
}

type PersonView struct {
	Name     string
	Items    []store.Item
	Editable bool
	Receiver *ReceiverView // non-nil only in Delivered|ChangePending
}

func (r *Round) editable(s State) bool {
	return s == Setup || s == Collecting || r.cfg.ListsEditableAfterDraw
}

// PersonView returns the page data for a person token (store.ErrNotFound if unknown).
func (r *Round) PersonView(ctx context.Context, token string) (PersonView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.st.PersonByToken(ctx, token)
	if err != nil {
		return PersonView{}, err
	}
	state, err := r.state(ctx, r.st)
	if err != nil {
		return PersonView{}, err
	}
	items, err := r.st.Items(ctx, p.ID)
	if err != nil {
		return PersonView{}, err
	}
	v := PersonView{Name: p.Name, Items: items, Editable: r.editable(state)}
	if state != Delivered && state != ChangePending {
		return v, nil
	}
	pairs, err := r.st.Pairs(ctx)
	if err != nil {
		return PersonView{}, err
	}
	rid, ok := pairs[p.ID]
	if !ok {
		return v, nil
	}
	people, err := r.st.People(ctx)
	if err != nil {
		return PersonView{}, err
	}
	for _, q := range people {
		if q.ID == rid {
			ritems, err := r.st.Items(ctx, q.ID)
			if err != nil {
				return PersonView{}, err
			}
			v.Receiver = &ReceiverView{Name: q.Name, Items: ritems}
		}
	}
	return v, nil
}

func validateItem(in ItemInput) (ItemInput, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Link = strings.TrimSpace(in.Link)
	in.Note = strings.TrimSpace(in.Note)
	switch {
	case in.Title == "":
		return in, &ItemError{"item.title_required"}
	case utf8.RuneCountInString(in.Title) > 200:
		return in, &ItemError{"item.title_too_long"}
	case utf8.RuneCountInString(in.Note) > 500:
		return in, &ItemError{"item.note_too_long"}
	}
	if in.Link != "" {
		u, err := url.Parse(in.Link)
		if err != nil || utf8.RuneCountInString(in.Link) > 500 ||
			(u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return in, &ItemError{"item.link_invalid"}
		}
	}
	return in, nil
}

// editPerson resolves the person and checks the list may be edited.
func (r *Round) editPerson(ctx context.Context, token string) (store.Person, error) {
	p, err := r.st.PersonByToken(ctx, token)
	if err != nil {
		return p, err
	}
	state, err := r.state(ctx, r.st)
	if err != nil {
		return p, err
	}
	if !r.editable(state) {
		return p, ErrNotEditable
	}
	return p, nil
}

func (r *Round) AddItem(ctx context.Context, token string, in ItemInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.editPerson(ctx, token)
	if err != nil {
		return err
	}
	in, err = validateItem(in)
	if err != nil {
		return err
	}
	_, err = r.st.AddItem(ctx, store.Item{PersonID: p.ID, Title: in.Title, Link: in.Link, Note: in.Note})
	return err
}

func (r *Round) UpdateItem(ctx context.Context, token string, id int64, in ItemInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.editPerson(ctx, token)
	if err != nil {
		return err
	}
	in, err = validateItem(in)
	if err != nil {
		return err
	}
	return r.st.UpdateItem(ctx, store.Item{ID: id, PersonID: p.ID, Title: in.Title, Link: in.Link, Note: in.Note})
}

func (r *Round) DeleteItem(ctx context.Context, token string, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.editPerson(ctx, token)
	if err != nil {
		return err
	}
	return r.st.DeleteItem(ctx, p.ID, id)
}
