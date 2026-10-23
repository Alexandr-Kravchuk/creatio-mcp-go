#pragma warning disable CS8618, // Non-nullable field is uninitialized.

using ATF.Repository;
using ATF.Repository.Attributes;
using System;
using System.Collections.Generic;
using System.Diagnostics.CodeAnalysis;
using System.ComponentModel.DataAnnotations;

namespace Acme.Models {
	[ExcludeFromCodeCoverage]
	[Schema("Country")]
	public class Country: BaseModel {

		#region Schema Columns

		#endregion

		#region Details

		/// <summary>
		/// Collection of Account by column CountryId
		/// <remarks>
		/// <see cref="global::Acme.Models.Account">See more about the Account model</see>
		/// </remarks>
		/// </summary>
		[DetailProperty(nameof(global::Acme.Models.Account.CountryId))]
		public virtual List<Account> CollectionOfAccountByCountry { get; set; }

		#endregion
	}
}
#pragma warning restore CS8618 // Non-nullable field is uninitialized.
