import os
from dataclasses import dataclass
import ldap
import ldap.modlist


LDAP_BASE = "dc=example,dc=org"


@dataclass(init=True)
class OpenLDAP:
    host: str
    admin_password: str
    ldap_base: str = LDAP_BASE


@dataclass(init=True)
class LDAPUser:
    uid: str
    password: str
    ldap_base: str = LDAP_BASE

    @property
    def username(self):
        return "uid={},{}".format(self.uid, self.ldap_base)


def create_ldap_user(server: OpenLDAP, user: LDAPUser):
    con = ldap.initialize(server.host)
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
