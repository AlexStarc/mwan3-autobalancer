AB_PACKAGE_DIR:=$(dir $(realpath $(lastword $(MAKEFILE_LIST))))
AB_SOURCE_DIR:=$(realpath $(AB_PACKAGE_DIR)/../..)

include $(TOPDIR)/rules.mk
PKG_NAME:=luci-app-mwan3-autobalancer
PKG_VERSION:=0.1.0
PKG_RELEASE:=2
PKG_LICENSE:=MIT
PKG_MAINTAINER:=Alex Starchenko

include $(INCLUDE_DIR)/package.mk

define Package/luci-app-mwan3-autobalancer
  SECTION:=luci
  CATEGORY:=LuCI
  SUBMENU:=3. Applications
  TITLE:=LuCI mwan3 autobalancer
  PKGARCH:=all
  DEPENDS:=+luci-base +mwan3-autobalancer
endef

define Build/Compile
endef

define Package/luci-app-mwan3-autobalancer/install
	$(INSTALL_DIR) $(1)/www/luci-static/resources/view/network
	$(INSTALL_DATA) $(AB_SOURCE_DIR)/openwrt/luci/view/network/mwan3-autobalancer.js $(1)/www/luci-static/resources/view/network/mwan3-autobalancer.js
	$(INSTALL_DIR) $(1)/usr/share/luci/menu.d $(1)/usr/share/rpcd/acl.d
	$(INSTALL_DATA) $(AB_SOURCE_DIR)/openwrt/luci/menu.d/luci-app-mwan3-autobalancer.json $(1)/usr/share/luci/menu.d/luci-app-mwan3-autobalancer.json
	$(INSTALL_DATA) $(AB_SOURCE_DIR)/openwrt/luci/acl.d/luci-app-mwan3-autobalancer.json $(1)/usr/share/rpcd/acl.d/luci-app-mwan3-autobalancer.json
endef

define Package/luci-app-mwan3-autobalancer/postinst
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] && exit 0
rm -f /tmp/luci-indexcache
rm -rf /tmp/luci-modulecache
/etc/init.d/rpcd reload
endef

define Package/luci-app-mwan3-autobalancer/postrm
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] && exit 0
rm -f /tmp/luci-indexcache
rm -rf /tmp/luci-modulecache
/etc/init.d/rpcd reload
endef

$(eval $(call BuildPackage,luci-app-mwan3-autobalancer))
