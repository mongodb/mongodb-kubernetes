from kubetester.kubetester import get_name, build_svc_fqdn


class OpsManagerCR(object):
    """ This is the class wrapping Ops Manager CR """

    def __init__(self, resource, namespace: str):
        self.resource = resource
        self.namespace = namespace

    def base_url(self):
        protocol = "http"
        svc_fqdn = build_svc_fqdn(self.svc_name(), self.namespace, self.get_cluster_name())
        return "{}://{}:{}".format(protocol, svc_fqdn, 8080)

    def name(self):
        return get_name(self.resource)

    def svc_name(self):
        return self.name() + "-svc"

    def api_key_secret(self):
        return self.name() + "-admin-key"

    def gen_key_secret(self):
        return self.name() + "-gen-key"

    def app_db_name(self):
        return self.name() + "-db"

    def get_spec(self):
        return self.resource["spec"]

    # getters for accessing different fields in the CR

    def get_cluster_name(self):
        if "clusterName" not in self.get_spec():
            return "cluster.local"
        return self.get_spec()["clusterName"]

    def get_admin_credentials(self):
        return self.get_spec()["adminCredentials"]

    def get_status(self):
        if "status" not in self.resource:
            return None
        return self.resource["status"]

    def get_om_status(self):
        if self.get_status() is None:
            return None
        return self.get_status()["opsManager"]

    def get_appdb_status(self):
        if self.get_status() is None:
            return None
        return self.get_status()["applicationDatabase"]
