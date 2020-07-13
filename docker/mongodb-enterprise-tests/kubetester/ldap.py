import os
from dataclasses import dataclass
import ldap
import ldap.modlist

from typing import Optional

LDAP_BASE = "dc=example,dc=org"


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


def create_user(server: OpenLDAP, user: LDAPUser, ca_path: Optional[str] = None):
    con = ldap.initialize(server.host)

    if server.host.startswith("ldaps://") and ca_path is not None:
        con.set_option(ldap.OPT_X_TLS_CACERTFILE, ca_path)
        con.set_option(ldap.OPT_X_TLS_NEWCTX, 0)

    dn_admin = "cn=admin," + server.ldap_base
    con.simple_bind_s(dn_admin, server.admin_password)

    modlist = {
        "objectClass": [b"inetOrgPerson", b"posixAccount", b"shadowAccount"],
        "userPassword": [str.encode(user.password)],
        "uid": [str.encode(user.uid)],
        "sn": [b"tests"],
        "cn": [b"Tests"],
        "displayName": [b"Tests"],
        "uidNumber": [b"5000"],
        "gidNumber": [b"10000"],
        "loginShell": [b"/bin/false"],
        "homeDirectory": [b"/home/auto"],
    }

    ldapmodlist = ldap.modlist.addModlist(modlist)

    con.add_s(user.username, ldapmodlist)
