AB_PACKAGE_DIR:=$(dir $(realpath $(lastword $(MAKEFILE_LIST))))
AB_SOURCE_DIR:=$(realpath $(AB_PACKAGE_DIR)/../..)

include $(TOPDIR)/rules.mk
PKG_NAME:=luci-app-mwan3-autobalancer
PKG_VERSION:=0.1.1
PKG_RELEASE:=1
PKG_LICENSE:=MIT
PKG_MAINTAINER:=Alex Starchenko
PKG_BUILD_DEPENDS:=luci-base/host

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
	$(INSTALL_DIR) $(PKG_BUILD_DIR)
	$(STAGING_DIR_HOSTPKG)/bin/po2lmo $(AB_SOURCE_DIR)/openwrt/luci/po/ru/mwan3-autobalancer.po $(PKG_BUILD_DIR)/mwan3-autobalancer.ru.lmo
endef

define Package/luci-app-mwan3-autobalancer/install
	$(INSTALL_DIR) $(1)/www/luci-static/resources/view/network
	$(INSTALL_DATA) $(AB_SOURCE_DIR)/openwrt/luci/view/network/mwan3-autobalancer.js $(1)/www/luci-static/resources/view/network/mwan3-autobalancer.js
	$(INSTALL_DIR) $(1)/usr/share/luci/menu.d $(1)/usr/share/rpcd/acl.d
	$(INSTALL_DATA) $(AB_SOURCE_DIR)/openwrt/luci/menu.d/luci-app-mwan3-autobalancer.json $(1)/usr/share/luci/menu.d/luci-app-mwan3-autobalancer.json
	$(INSTALL_DATA) $(AB_SOURCE_DIR)/openwrt/luci/acl.d/luci-app-mwan3-autobalancer.json $(1)/usr/share/rpcd/acl.d/luci-app-mwan3-autobalancer.json
	$(INSTALL_DIR) $(1)/usr/lib/lua/luci/i18n $(1)/etc/uci-defaults
	$(INSTALL_DATA) $(PKG_BUILD_DIR)/mwan3-autobalancer.ru.lmo $(1)/usr/lib/lua/luci/i18n/mwan3-autobalancer.ru.lmo
	$(INSTALL_DATA) $(AB_SOURCE_DIR)/openwrt/luci/uci-defaults/40-mwan3-autobalancer-language $(1)/etc/uci-defaults/40-mwan3-autobalancer-language
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
