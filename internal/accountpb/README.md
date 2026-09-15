# accountpb — vendored account-core contracts

A copy of `account/proto/openpak/account/v1` (generated stubs + the `.proto` sources they
come from), the same way the website, nn-friends and the Switch adapter carry it: the
account core is a separate private repository, and a `replace => ../account` resolves only
inside the local workspace, so a clean checkout and CI could not build. To refresh: re-copy
the files.
