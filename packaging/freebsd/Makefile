# FreeBSD port skeleton for zxplore. OpenZFS is in base; the GUI needs the
# GL/X11/Wayland stack, the TUI binary is static. Build uses gmake (the
# project Makefile is GNU make).
PORTNAME=	zxplore
DISTVERSION=	1.1.0
CATEGORIES=	sysutils
MAINTAINER=	admin@zxplore.dev
COMMENT=	Keyboard-driven console for OpenZFS primitives
WWW=		https://github.com/zxplore/zxplore

LICENSE=	BSD3CLAUSE
LICENSE_FILE=	${WRKSRC}/LICENSE

USES=		gl gmake go pkgconfig xorg
USE_GL=		gl
USE_XORG=	x11 xcursor xrandr xinerama xi xxf86vm
LIB_DEPENDS=	libxkbcommon.so:x11/libxkbcommon \
		libwayland-client.so:graphics/wayland \
		libfontconfig.so:x11-fonts/fontconfig

USE_GITHUB=	yes
GH_ACCOUNT=	zxplore

PLIST_FILES=	bin/zxplore \
		bin/zxplore-tui \
		bin/zxplore-api \
		bin/zxplore-txn \
		share/applications/zxplore.desktop \
		share/applications/zxplore-tui.desktop \
		share/icons/hicolor/scalable/apps/zxplore.svg \
		share/icons/hicolor/scalable/apps/zxplore-tui.svg \
		share/doc/zxplore/README.md \
		share/doc/zxplore/DESIGN.md \
		share/man/man1/zxplore.1.gz

do-build:
	cd ${WRKSRC} && ${SETENV} ${MAKE_ENV} gmake build

do-install:
	cd ${WRKSRC} && ${SETENV} gmake DESTDIR=${STAGEDIR} PREFIX=${PREFIX} install

.include <bsd.port.mk>
