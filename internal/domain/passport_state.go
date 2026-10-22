package domain

import "slices"

func (d *PassportUserData) SaveElement(el PassportElement) {
	for i := range d.Elements {
		if d.Elements[i].Type == el.Type {
			d.Elements[i] = el
			return
		}
	}
	d.Elements = append(d.Elements, el)
}
func (d *PassportUserData) DeleteElements(types []string) {
	d.Elements = slices.DeleteFunc(d.Elements, func(el PassportElement) bool { return slices.Contains(types, el.Type) })
	// Revocation removes the bot's authority to return new errors for the deleted data.
	for i := range d.Authorizations {
		a := &d.Authorizations[i]
		a.Elements = slices.DeleteFunc(a.Elements, func(el PassportElement) bool { return slices.Contains(types, el.Type) })
		a.Errors = slices.DeleteFunc(a.Errors, func(e PassportError) bool { return slices.Contains(types, e.Type) })
	}
}
func (d *PassportUserData) Authorize(a PassportAuthorization) error {
	if a.BotUserID <= 0 || len(a.Elements) == 0 || len(a.CredentialsData) == 0 || len(a.CredentialsHash) != 32 || len(a.CredentialsSecret) == 0 {
		return ErrPassportInvalid
	}
	seen := map[string]bool{}
	for index, el := range a.Elements {
		found := false
		for _, current := range d.Elements {
			if current.Type == el.Type && current.Hash == el.Hash && current.SecretID == el.SecretID {
				a.Elements[index] = current
				found = true
				break
			}
		}
		if !found || seen[el.Type] {
			return ErrPassportInvalid
		}
		seen[el.Type] = true
	}
	for i, old := range d.Authorizations {
		if old.BotUserID != a.BotUserID {
			continue
		}
		for _, e := range old.Errors {
			for _, el := range d.Elements {
				if el.Type != e.Type {
					continue
				}
				for _, prev := range old.Elements {
					if prev.Type == e.Type && PassportErrorUnchanged(prev, el, e) {
						return ErrPassportErrorsPending
					}
				}
			}
		}
		d.Authorizations[i] = a
		return nil
	}
	d.Authorizations = append(d.Authorizations, a)
	return nil
}
func (d *PassportUserData) SetErrors(botID int64, errs []PassportError) error {
	for i := range d.Authorizations {
		a := &d.Authorizations[i]
		if a.BotUserID != botID {
			continue
		}
		if err := ValidatePassportErrors(a.Elements, errs); err != nil {
			return err
		}
		// Never attach a stale rejection to data that has already been replaced.
		for _, e := range errs {
			var old, current *PassportElement
			for j := range a.Elements {
				if a.Elements[j].Type == e.Type {
					old = &a.Elements[j]
				}
			}
			for j := range d.Elements {
				if d.Elements[j].Type == e.Type {
					current = &d.Elements[j]
				}
			}
			if old == nil || current == nil || !PassportErrorUnchanged(*old, *current, e) {
				return ErrPassportInvalid
			}
		}
		a.Errors = errs
		return nil
	}
	return ErrPassportInvalid
}
