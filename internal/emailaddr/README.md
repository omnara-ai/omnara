# Email lookup keys

`Normalize` produces account lookup keys using current Unicode tables and
[UTS #46 nontransitional domain processing](https://www.unicode.org/reports/tr46/#IDNA_Mapping_Table).
Local-part lowercasing is Omnara's account policy. Keep the original address for
display and delivery. Domain-conversion errors preserve the original domain with
ASCII letters lowercased; this function does not validate email addresses.

Toolchain and library upgrades can change email matching even when module
versions stay pinned. Review relevant behavior changes during upgrades; if they
affect account lookup, check existing keys and conflicting matches before rollout.
