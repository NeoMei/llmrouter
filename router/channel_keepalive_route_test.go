/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package router

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/service/authz"
)

func TestChannelReachabilityAndPaidGenerationRoutesAreSeparated(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodGet, "/test", authz.ChannelOperate, controller.TestAllChannels)
	assertChannelRoutePermission(t, http.MethodGet, "/test/:id", authz.ChannelOperate, controller.TestChannel)
	assertChannelRoutePermission(t, http.MethodPost, "/test/:id/deep", authz.ChannelSensitiveWrite, controller.TestChannelDeep)
}
