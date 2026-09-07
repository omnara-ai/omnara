# Email lookup keys

`Normalize` produces account lookup keys using current Unicode tables and
[UTS #46 nontransitional domain processing](https://www.unicode.org/reports/tr46/#IDNA_Mapping_Table).
Local-part lowercasing is Omnara's account policy. Keep the original address for
display and delivery. Domain-conversion errors preserve the original domain with
ASCII letters lowercased; this function does not validate email addresses.

Go 1.27 changes the reviewed baseline from Unicode 15 to Unicode 17. In domain
names, uppercase `ẞ` now matches lowercase `ß`, remaining distinct from ASCII `ss`.
Some previously disallowed characters, such as U+3164 HANGUL FILLER, are now ignored.

Go, `x/net`, and `x/text` upgrades can change mappings, casing, or domain validity.
A Go toolchain upgrade alone can select new tables in otherwise pinned modules.
`TestReviewedUnicodeVersions` records the reviewed casing, IDNA, normalization,
and bidirectional-text table releases.
Review behavior changes even when those version constants stay unchanged.

Before adopting changed rules, compare stored email keys and observed login
spellings under both versions, including scoped uniqueness collisions and changes
in which account a spelling matches. A clean stored-row comparison alone does
not establish unchanged lookup behavior. Coordinate the final check and cutover
so old and new writers do not overlap; reassess rollback after new writes.
