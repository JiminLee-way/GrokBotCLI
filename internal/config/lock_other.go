//go:build !unix

package config

func (p *Profile) Lock() (unlock func(), err error) { return func() {}, nil }
