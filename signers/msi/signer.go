/*
 * Copyright (c) SAS Institute Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package msi

// Sign Microsoft Installer files

import (
	"io"
	"io/ioutil"
	"os"

	"gerrit-pdt.unx.sas.com/tools/relic.git/lib/atomicfile"
	"gerrit-pdt.unx.sas.com/tools/relic.git/lib/authenticode"
	"gerrit-pdt.unx.sas.com/tools/relic.git/lib/certloader"
	"gerrit-pdt.unx.sas.com/tools/relic.git/lib/comdoc"
	"gerrit-pdt.unx.sas.com/tools/relic.git/lib/magic"
	"gerrit-pdt.unx.sas.com/tools/relic.git/signers"
	"gerrit-pdt.unx.sas.com/tools/relic.git/signers/pkcs"
)

var MsiSigner = &signers.Signer{
	Name:      "msi",
	Aliases:   []string{"msi-tar"},
	Magic:     magic.FileTypeMSI,
	CertTypes: signers.CertTypeX509,
	Transform: transform,
	Sign:      sign,
	Verify:    verify,
}

func init() {
	MsiSigner.Flags().Bool("no-extended-sig", false, "(MSI) Don't emit a MsiDigitalSignatureEx digest")
	signers.Register(MsiSigner)
}

type msiTransformer struct {
	f     *os.File
	cdf   *comdoc.ComDoc
	exsig []byte
}

func transform(f *os.File, opts signers.SignOpts) (signers.Transformer, error) {
	cdf, err := comdoc.ReadFile(f)
	if err != nil {
		return nil, err
	}
	var exsig []byte
	noExtended, _ := opts.Flags.GetBool("no-extended-sig")
	if !noExtended {
		exsig, err = authenticode.PrehashMSI(cdf, opts.Hash)
		if err != nil {
			return nil, err
		}
	}
	return &msiTransformer{f, cdf, exsig}, nil
}

// transform the MSI to a tar stream for upload
func (t *msiTransformer) GetReader() (io.Reader, int64, error) {
	r, w := io.Pipe()
	go func() {
		w.CloseWithError(authenticode.MsiToTar(t.cdf, w))
	}()
	return r, -1, nil
}

// apply a signed PKCS#7 blob to an already-open MSI document
func (t *msiTransformer) Apply(dest, mimeType string, result io.Reader) error {
	blob, err := ioutil.ReadAll(result)
	if err != nil {
		return err
	}
	// copy src to dest if needed, otherwise open in-place
	f, err := atomicfile.WriteInPlace(t.f, dest)
	if err != nil {
		return err
	}
	defer f.Close()
	cdf, err := comdoc.WriteFile(f.GetFile())
	if err != nil {
		return err
	}
	if err := authenticode.InsertMSISignature(cdf, blob, t.exsig); err != nil {
		return err
	}
	if err := cdf.Close(); err != nil {
		return err
	}
	return f.Commit()
}

// sign a transformed tarball and return the PKCS#7 blob
func sign(r io.Reader, cert *certloader.Certificate, opts signers.SignOpts) ([]byte, error) {
	noExtended, _ := opts.Flags.GetBool("no-extended-sig")
	sum, err := authenticode.DigestMsiTar(r, opts.Hash, !noExtended)
	if err != nil {
		return nil, err
	}
	psd, err := authenticode.SignMSIImprint(sum, opts.Hash, cert.Signer(), cert.Chain())
	if err != nil {
		return nil, err
	}
	return pkcs.Timestamp(psd, cert, opts, true)
}

func verify(f *os.File, opts signers.VerifyOpts) ([]*signers.Signature, error) {
	sig, err := authenticode.VerifyMSI(f, opts.NoDigests)
	if err != nil {
		return nil, err
	}
	return []*signers.Signature{&signers.Signature{
		Hash:          sig.HashFunc,
		X509Signature: &sig.TimestampedSignature,
	}}, nil
}