# gopp — Postfix policy server in Go

## gopp build and install

Build and install is a simple process:
```bash
go get github.com/bradfitz/gomemcache/memcache
sudo make install
```
to build gopp and install it into /usr/local/sbin directory

If your are use Debian/Ubibtu you also need to copy defaults to /etc:
```bash
cp scripts/gopp-etc_default /etc/defaults/gopp
```
and then
```bash
cp scripts/gopp.conf.upstart /etc/init
```
if you prefer Upstart of
```bash
cp scripts/init_d.ubuntu /etc/init.d/gopp

update-rc.d gopp defaults
```
in case of System V startup scripts are preferred.

## Postfix configuration
Edit /etc/postfix/main.cf and append `check_policy_service` to you `smtpd_recipient_restrictions` checklist. Please note `check_policy_service` should be one of the late element of the `smtpd_recipient_restrictions` list, for example:
```
smtpd_recipient_restrictions = permit_mynetworks
        permit_sasl_authenticated
        reject_unauth_destination
        check_policy_service inet:127.0.0.1:10033
```
