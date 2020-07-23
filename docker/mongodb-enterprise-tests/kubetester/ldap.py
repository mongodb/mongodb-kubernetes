import os
from dataclasses import dataclass
import ldap
import ldap.modlist

from typing import Optional

LDAP_BASE = "dc=example,dc=org"
LDAP_AUTHENTICATION_MECHANISM = "PLAIN"


@dataclass(init=True)
class OpenLDAP:
    host: str
    admin_password: str
    ldap_base: str = LDAP_BASE

    @property
    def servers(self):
        return self.host.partition("//")[2]


@dataclass(init=True)
class LDAPUser:
    uid: str
    password: str
    ldap_base: str = LDAP_BASE

    @property
    def username(self):
        return "uid={},{}".format(self.uid, self.ldap_base)


def create_user(
    server: OpenLDAP,
    user: LDAPUser,
    ca_path: Optional[str] = None,
    ou: Optional[str] = None,
):
    """Creates a new user in the LDAP database. It might include an optional organizational unit (ou)."""
    con = ldap_initialize(server, ca_path)

    modlist = {
        "objectClass": [b"top", b"account", b"simpleSecurityObject"],
        "userPassword": [str.encode(user.password)],
        "uid": [str.encode(user.uid)],
    }
    ldapmodlist = ldap.modlist.addModlist(modlist)

    dn = build_dn(uid=user.uid, ou=ou, base=server.ldap_base)
    con.add_s(dn, ldapmodlist)


def create_organizational_unit(
    server: OpenLDAP, ou: str, ca_path: Optional[str] = None
):
    """Creates an organizational unit."""
    con = ldap_initialize(server, ca_path)

    modlist = {"objectClass": [b"top", b"organizationalUnit"], "ou": [str.encode(ou)]}

    ldapmodlist = ldap.modlist.addModlist(modlist)

    dn = build_dn(ou=ou, base=server.ldap_base)
    con.add_s(dn, ldapmodlist)


def create_group(server: OpenLDAP, cn: str, ou: str, ca_path: Optional[str] = None):
    """Creates a group in the LDAP database, that also belongs to an organizational unit. By default, it adds
    the admin user to it."""
    con = ldap_initialize(server, ca_path)

    unique_member = build_dn(base=server.ldap_base, uid="admin", ou=ou)
    modlist = {
        "objectClass": [b"top", b"groupOfUniqueNames"],
        "cn": str.encode(cn),
        "uniqueMember": str.encode(unique_member),
    }
    ldapmodlist = ldap.modlist.addModlist(modlist)

    dn = build_dn(base=server.ldap_base, cn=cn, ou=ou)
    con.add_s(dn, ldapmodlist)


def add_user_to_group(
    server: OpenLDAP, user: str, group_cn: str, ou: str, ca_path: Optional[str] = None
):
    """Adds a new uniqueMember to a group, this is equivalent to add a user to the group."""
    con = ldap_initialize(server, ca_path)

    unique_member = build_dn(uid=user, ou=ou, base=server.ldap_base)
    modlist = {"uniqueMember": [str.encode(unique_member)]}
    ldapmodlist = ldap.modlist.modifyModlist({}, modlist)

    dn = build_dn(cn=group_cn, ou=ou, base=server.ldap_base)
    con.modify_s(dn, ldapmodlist)


def ldap_initialize(server: OpenLDAP, ca_path: Optional[str] = None):
    con = ldap.initialize(server.host)

    if server.host.startswith("ldaps://") and ca_path is not None:
        con.set_option(ldap.OPT_X_TLS_CACERTFILE, ca_path)
        con.set_option(ldap.OPT_X_TLS_NEWCTX, 0)

    dn_admin = build_dn(cn="admin", base=server.ldap_base)
    con.simple_bind_s(dn_admin, server.admin_password)
    return con


def build_dn(base: Optional[str] = None, **kwargs):
    """Builds a distinguished name from arguments."""
    dn = ",".join("{}={}".format(k, v) for k, v in kwargs.items() if v is not None)
    if base is not None:
        dn += "," + base

    return dn
